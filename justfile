build:
    go build -o holocron ./cmd/holocron

test:
    go test ./...

install:
    go install ./cmd/holocron

run *args:
    go run ./cmd/holocron {{args}}

clean:
    rm -f holocron
