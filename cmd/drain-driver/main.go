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

// The kubectl-server-side-drain-driver is a Specialized Lifecycle Management (SLM)
// driver that implements server-side node drain. It registers with the kubelet as an
// SLM plugin and publishes a LifecycleTransition for doing node drain: cordon → drain.
// A second LifecycleTransition is used for returning the Node: maintenance → uncordon.
//
// The kubelet automatically patches the Node condition based on the
// work the driver is doing.
package main

import (
	"os"

	"k8s.io/component-base/cli"
	_ "k8s.io/component-base/logs/json/register"
	"k8s.io/kubectl-server-side-drain/pkg/plugin"
)

func main() {
	command := plugin.NewCommand()
	code := cli.Run(command)
	os.Exit(code)
}
