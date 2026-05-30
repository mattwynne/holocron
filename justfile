build:
    go build -o holocron ./cmd/holocron
    @if [ -f "$HOME/Library/LaunchAgents/net.mattwynne.holocron.plist" ]; then \
        echo "Updating holocron daemon"; \
        ./holocron service install; \
    else \
        echo "Holocron service not installed; skipping daemon update"; \
    fi

test:
    go test ./...

install:
    go install ./cmd/holocron
    @bin="$${GOBIN:-$$(go env GOPATH)/bin}/holocron"; \
    if [ -f "$HOME/Library/LaunchAgents/net.mattwynne.holocron.plist" ]; then \
        echo "Updating holocron daemon"; \
        "$$bin" service install; \
    else \
        echo "Holocron service not installed; skipping daemon update"; \
    fi

run *args:
    go run ./cmd/holocron {{args}}

clean:
    rm -f holocron
