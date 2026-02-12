.PHONY: build
build: ## Build the drain-driver binary (static, linux/amd64).
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/drain-driver ./cmd/drain-driver

.PHONY: drain
drain: ## Drain a node. Usage: make drain NODE=<node-name>
ifndef NODE
	$(error Usage: make drain NODE=<node-name>)
endif
	@echo '{"apiVersion":"lifecycle.k8s.io/v1alpha1","kind":"LifecycleEvent","metadata":{"name":"drain-$(NODE)"},"spec":{"transitionName":"drain.slm.k8s.io-drain","bindingNode":"$(NODE)"}}' | kubectl apply -f -

.PHONY: maintenance-complete
maintenance-complete: ## Uncordon a node after maintenance. Usage: make maintenance-complete NODE=<node-name>
ifndef NODE
	$(error Usage: make maintenance-complete NODE=<node-name>)
endif
	@echo '{"apiVersion":"lifecycle.k8s.io/v1alpha1","kind":"LifecycleEvent","metadata":{"name":"uncordon-$(NODE)"},"spec":{"transitionName":"drain.slm.k8s.io-maintenance-complete","bindingNode":"$(NODE)"}}' | kubectl apply -f -

.PHONY: clean
clean: ## Delete the LifecycleTransitions created by this driver.
	kubectl delete lifecycletransition drain.slm.k8s.io --ignore-not-found
	kubectl delete lifecycletransition drain.slm.k8s.io-uncordon --ignore-not-found
