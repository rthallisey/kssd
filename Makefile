.PHONY: build
build: ## Build the drain-driver binary.
	go build -o bin/drain-driver ./cmd/drain-driver

.PHONY: drain
drain: ## Drain a node. Usage: make drain NODE=<node-name>
ifndef NODE
	$(error Usage: make drain NODE=<node-name>)
endif
	kubectl apply -f - <<< '{ \
	  "apiVersion": "lifecycle.k8s.io/v1alpha1", \
	  "kind": "LifecycleEvent", \
	  "metadata": { "name": "drain-$(NODE)" }, \
	  "spec": { "transitionName": "drain.slm.k8s.io", "bindingNode": "$(NODE)" } \
	}'

.PHONY: maintenance-complete
maintenance-complete: ## Uncordon a node after maintenance. Usage: make maintenance-complete NODE=<node-name>
ifndef NODE
	$(error Usage: make maintenance-complete NODE=<node-name>)
endif
	kubectl apply -f - <<< '{ \
	  "apiVersion": "lifecycle.k8s.io/v1alpha1", \
	  "kind": "LifecycleEvent", \
	  "metadata": { "name": "uncordon-$(NODE)" }, \
	  "spec": { "transitionName": "drain.slm.k8s.io-uncordon", "bindingNode": "$(NODE)" } \
	}'

.PHONY: clean
clean: ## Delete the LifecycleTransitions created by this driver.
	kubectl delete lifecycletransition drain.slm.k8s.io --ignore-not-found
	kubectl delete lifecycletransition drain.slm.k8s.io-uncordon --ignore-not-found
