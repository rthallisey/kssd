/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package driver implements the server-side kubectl drain.
//
// The driver handles two lifecycle transitions:
//
// Drain (drain-started → drain-complete):
//   - StartLifecycleTransition: cordons the node and begins async pod eviction.
//   - EndLifecycleTransition: watches for remaining evictable pods and returns
//     drain-complete when all pods are gone.
//
// Uncordon (uncordoning → maintenance-complete):
//   - StartLifecycleTransition: uncordons the node (sets spec.unschedulable = false).
//   - EndLifecycleTransition: verifies the node is schedulable and returns
//     maintenance-complete.
package driver

import (
	"context"
	"fmt"
	"sync"
	"time"

	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	slmpbv1alpha1 "k8s.io/kubelet/pkg/apis/slm/v1alpha1"
)

// Lifecycle condition constants shared between the driver and command package.
const (
	// Drain transition conditions.
	DrainStarted  = "drain-started"
	DrainComplete = "drain-complete"

	// Uncordon transition conditions.
	Uncordoning         = "uncordoning"
	MaintenanceComplete = "maintenance-complete"
)

// evictionGoroutineTimeout for the async eviction.
const evictionGoroutineTimeout = 10 * time.Minute

// DrainService implements slmpbv1alpha1.SLMPluginServer with real drain logic.
type DrainService struct {
	slmpbv1alpha1.UnimplementedSLMPluginServer

	kubeClient      kubernetes.Interface
	nodeName        string
	evictionTimeout time.Duration
	gracePeriod     int64 // -1 = use pod default

	// Track whether we already started draining for a given event.
	mu             sync.Mutex
	activeEvent    string
	evictionErrors map[string]string // podKey -> last error
}

// NewDrainService creates a new DrainService.
func NewDrainService(kubeClient kubernetes.Interface, nodeName string, evictionTimeout time.Duration, gracePeriod int64) *DrainService {
	return &DrainService{
		kubeClient:      kubeClient,
		nodeName:        nodeName,
		evictionTimeout: evictionTimeout,
		gracePeriod:     gracePeriod,
		evictionErrors:  make(map[string]string),
	}
}

// StartLifecycleTransition is called by the kubelet after it claims a
// LifecycleEvent. The driver dispatches based on the transition type:
//
// Drain transition (start="drain-started"):
//  1. Cordons the node.
//  2. Kicks off async pod evictions.
//  3. Returns the "drain-started" condition immediately.
//
// Uncordon transition (start="uncordoning"):
//  1. Uncordons the node (sets spec.unschedulable = false).
//  2. Returns the "uncordoning" condition.
func (d *DrainService) StartLifecycleTransition(ctx context.Context, req *slmpbv1alpha1.StartLifecycleTransitionRequest) (*slmpbv1alpha1.LifecycleTransitionResponse, error) {
	logger := klog.FromContext(ctx)
	logger.Info("StartLifecycleTransition called",
		"transition", req.GetTransitionName(),
		"event", req.GetEventName(),
		"node", req.GetNodeName(),
		"start", req.GetStart(),
	)

	targetNode := req.GetNodeName()
	if targetNode == "" {
		targetNode = d.nodeName
	}

	transition := req.GetStart()
	switch transition {
	case Uncordoning:
		return d.startUncordon(ctx, req, targetNode)
	case DrainStarted:
		return d.startDrain(ctx, req, targetNode)
	default:
		return nil, fmt.Errorf("driver does not support transition %q in StartLifecycleTransition", transition)
	}
}

// startDrain cordons the node, kicks off async evictions, and returns
// the drain-started condition.
func (d *DrainService) startDrain(ctx context.Context, req *slmpbv1alpha1.StartLifecycleTransitionRequest, targetNode string) (*slmpbv1alpha1.LifecycleTransitionResponse, error) {
	logger := klog.FromContext(ctx)

	d.mu.Lock()
	d.activeEvent = req.GetEventName()
	d.evictionErrors = make(map[string]string)
	d.mu.Unlock()

	// Cordon the node
	if err := d.cordonNode(ctx, targetNode); err != nil {
		return &slmpbv1alpha1.LifecycleTransitionResponse{
			NodeName: targetNode,
			Error:    fmt.Sprintf("cordon node: %v", err),
		}, nil
	}
	logger.Info("Node cordoned", "node", targetNode)

	// Start an async eviction so the gRPC call
	// returns immediately. The kubelet will call EndLifecycleTransition
	// on the next reconcile which will monitor drain progress.
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), evictionGoroutineTimeout)
		defer cancel()
		evicted, failed, total := d.evictAllPods(bgCtx, targetNode)
		klog.FromContext(bgCtx).Info("Background eviction pass complete",
			"node", targetNode,
			"total", total,
			"evicted", evicted,
			"failed", failed,
		)
	}()

	// Return the start condition.
	return &slmpbv1alpha1.LifecycleTransitionResponse{
		LifecycleCondition: req.GetStart(),
		NodeName:           targetNode,
	}, nil
}

// startUncordon uncordons the node and returns the uncordoning condition.
func (d *DrainService) startUncordon(ctx context.Context, req *slmpbv1alpha1.StartLifecycleTransitionRequest, targetNode string) (*slmpbv1alpha1.LifecycleTransitionResponse, error) {
	logger := klog.FromContext(ctx)

	if err := d.uncordonNode(ctx, targetNode); err != nil {
		return &slmpbv1alpha1.LifecycleTransitionResponse{
			NodeName: targetNode,
			Error:    fmt.Sprintf("uncordon node: %v", err),
		}, nil
	}
	logger.Info("Node uncordoned", "node", targetNode)

	return &slmpbv1alpha1.LifecycleTransitionResponse{
		LifecycleCondition: req.GetStart(),
		NodeName:           targetNode,
	}, nil
}

// EndLifecycleTransition is called by the kubelet to check whether a
// transition has completed. It dispatches based on the transition type:
//
// Drain transition (end="drain-complete"):
//
//	Watches for remaining evictable pods; returns drain-complete when done.
//
// Uncordon transition (end="maintenance-complete"):
//
//	Verifies the node is schedulable and returns maintenance-complete.
func (d *DrainService) EndLifecycleTransition(ctx context.Context, req *slmpbv1alpha1.EndLifecycleTransitionRequest) (*slmpbv1alpha1.LifecycleTransitionResponse, error) {
	logger := klog.FromContext(ctx)
	logger.Info("EndLifecycleTransition called",
		"transition", req.GetTransitionName(),
		"event", req.GetEventName(),
		"node", req.GetNodeName(),
		"end", req.GetEnd(),
	)

	targetNode := req.GetNodeName()
	if targetNode == "" {
		targetNode = d.nodeName
	}

	transition := req.GetEnd()
	switch transition {
	case MaintenanceComplete:
		return d.endUncordon(ctx, req, targetNode)
	case DrainComplete:
		return d.endDrain(ctx, req, targetNode)
	default:
		return nil, fmt.Errorf("driver does not support transition %q in EndLifecycleTransition", transition)
	}
}

// endDrain checks if evictable pods remain. If none, returns drain-complete.
func (d *DrainService) endDrain(ctx context.Context, req *slmpbv1alpha1.EndLifecycleTransitionRequest, targetNode string) (*slmpbv1alpha1.LifecycleTransitionResponse, error) {
	logger := klog.FromContext(ctx)

	pods, err := d.listEvictablePods(ctx, targetNode)
	if err != nil {
		return &slmpbv1alpha1.LifecycleTransitionResponse{
			NodeName: targetNode,
			Error:    fmt.Sprintf("list pods: %v", err),
		}, nil
	}

	if len(pods) == 0 {
		logger.Info("All pods evicted, drain complete", "node", targetNode)
		d.mu.Lock()
		d.activeEvent = ""
		d.mu.Unlock()

		return &slmpbv1alpha1.LifecycleTransitionResponse{
			LifecycleCondition: req.GetEnd(),
			NodeName:           targetNode,
		}, nil
	}

	// Pods still remain — the background eviction goroutine is working
	// on them. Report the count and return the start condition so the
	// kubelet calls again on the next tick.
	logger.Info("Waiting for drain to complete",
		"node", targetNode,
		"remaining", len(pods),
	)

	return &slmpbv1alpha1.LifecycleTransitionResponse{
		LifecycleCondition: req.GetStart(),
		NodeName:           targetNode,
	}, nil
}

// endUncordon verifies the node is schedulable and returns
// maintenance-complete. If the node is still unschedulable (e.g. the
// uncordon was interrupted), it retries and returns uncordoning.
func (d *DrainService) endUncordon(ctx context.Context, req *slmpbv1alpha1.EndLifecycleTransitionRequest, targetNode string) (*slmpbv1alpha1.LifecycleTransitionResponse, error) {
	logger := klog.FromContext(ctx)

	node, err := d.kubeClient.CoreV1().Nodes().Get(ctx, targetNode, metav1.GetOptions{})
	if err != nil {
		return &slmpbv1alpha1.LifecycleTransitionResponse{
			NodeName: targetNode,
			Error:    fmt.Sprintf("get node: %v", err),
		}, nil
	}

	if !node.Spec.Unschedulable {
		logger.Info("Node is schedulable, maintenance complete", "node", targetNode)
		return &slmpbv1alpha1.LifecycleTransitionResponse{
			LifecycleCondition: req.GetEnd(),
			NodeName:           targetNode,
		}, nil
	}

	// Node is still unschedulable — retry uncordon.
	logger.Info("Node still unschedulable, retrying uncordon", "node", targetNode)
	if err := d.uncordonNode(ctx, targetNode); err != nil {
		return &slmpbv1alpha1.LifecycleTransitionResponse{
			NodeName: targetNode,
			Error:    fmt.Sprintf("uncordon node: %v", err),
		}, nil
	}

	return &slmpbv1alpha1.LifecycleTransitionResponse{
		LifecycleCondition: req.GetStart(),
		NodeName:           targetNode,
	}, nil
}

// cordonNode sets spec.unschedulable = true on the target node.
func (d *DrainService) cordonNode(ctx context.Context, nodeName string) error {
	node, err := d.kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if node.Spec.Unschedulable {
		return nil // already cordoned
	}
	node.Spec.Unschedulable = true
	_, err = d.kubeClient.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	return err
}

// uncordonNode sets spec.unschedulable = false on the target node.
func (d *DrainService) uncordonNode(ctx context.Context, nodeName string) error {
	node, err := d.kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !node.Spec.Unschedulable {
		return nil // already schedulable
	}
	node.Spec.Unschedulable = false
	_, err = d.kubeClient.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	return err
}

// podInfo holds the name and namespace of a pod for eviction.
type podInfo struct {
	Name      string
	Namespace string
}

// listEvictablePods returns all pods on the node that should be evicted.
// It excludes mirror pods (owned by the kubelet) and DaemonSet pods.
func (d *DrainService) listEvictablePods(ctx context.Context, nodeName string) ([]podInfo, error) {
	podList, err := d.kubeClient.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: fields.SelectorFromSet(fields.Set{
			"spec.nodeName": nodeName,
		}).String(),
	})
	if err != nil {
		return nil, err
	}

	var evictable []podInfo
	for _, pod := range podList.Items {
		// Skip mirror pods (static pods managed by the kubelet).
		if _, isMirror := pod.Annotations["kubernetes.io/config.mirror"]; isMirror {
			continue
		}

		// Skip DaemonSet-managed pods — they will be rescheduled to the
		// same node immediately, so evicting them is counterproductive.
		isDaemonSet := false
		for _, ref := range pod.OwnerReferences {
			if ref.Kind == "DaemonSet" {
				isDaemonSet = true
				break
			}
		}
		if isDaemonSet {
			continue
		}

		// Skip pods that are already terminating.
		if pod.DeletionTimestamp != nil {
			continue
		}

		// Skip pods in Succeeded or Failed phase.
		if pod.Status.Phase == "Succeeded" || pod.Status.Phase == "Failed" {
			continue
		}

		evictable = append(evictable, podInfo{
			Name:      pod.Name,
			Namespace: pod.Namespace,
		})
	}
	return evictable, nil
}

// evictAllPods lists evictable pods and evicts each one. It returns the
// count of successfully evicted, failed, and total pods.
func (d *DrainService) evictAllPods(ctx context.Context, nodeName string) (evicted, failed, total int) {
	logger := klog.FromContext(ctx)

	pods, err := d.listEvictablePods(ctx, nodeName)
	if err != nil {
		logger.Error(err, "Failed to list pods for eviction")
		return 0, 0, 0
	}
	total = len(pods)

	for _, p := range pods {
		if err := d.evictPod(ctx, p); err != nil {
			logger.V(3).Info("Eviction failed",
				"pod", p.Namespace+"/"+p.Name,
				"err", err,
			)
			d.mu.Lock()
			d.evictionErrors[p.Namespace+"/"+p.Name] = err.Error()
			d.mu.Unlock()
			failed++
		} else {
			logger.V(3).Info("Pod evicted", "pod", p.Namespace+"/"+p.Name)
			evicted++
		}
	}
	return evicted, failed, total
}

// evictPod sends an Eviction for a single pod.
func (d *DrainService) evictPod(ctx context.Context, p podInfo) error {
	eviction := &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{
			Name:      p.Name,
			Namespace: p.Namespace,
		},
		DeleteOptions: d.deleteOptions(),
	}
	err := d.kubeClient.CoreV1().Pods(p.Namespace).EvictV1(ctx, eviction)
	if apierrors.IsNotFound(err) {
		return nil // pod already gone
	}
	return err
}

// deleteOptions returns the metav1.DeleteOptions for evictions, honouring
// the configured grace period.
func (d *DrainService) deleteOptions() *metav1.DeleteOptions {
	opts := &metav1.DeleteOptions{}
	if d.gracePeriod >= 0 {
		opts.GracePeriodSeconds = &d.gracePeriod
	}
	return opts
}
