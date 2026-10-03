build:
	@go build -o bin/blockchain .

## run: start the three-node demo network and the demo wallet
run: build
	@./bin/blockchain

test:
	@go test -race ./...

## proto: regenerate the gRPC code after changing proto/types.proto
proto:
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/*.proto

.PHONY: build run test proto
