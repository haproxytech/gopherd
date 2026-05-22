.PHONY: % ci

ci:
	task ci

%:
	task $@

install-task:
	go install github.com/go-task/task/v3/cmd/task@latest
