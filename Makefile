DEFAULT:compile

LD_FLAGS := "-X main.GitCommit=`git rev-parse HEAD` -X main.GitTag=`git symbolic-ref -q --short HEAD || git describe --tags --exact-match || git rev-parse HEAD`"

compile:
	@echo "\nCompiling poh-check-car binary for current platform ..."
	go build -ldflags=$(LD_FLAGS) -o ./bin/poh-check-car .
