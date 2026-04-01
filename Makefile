BIN_DIR := bin
TARGET := a2a_samples

.PHONY: all build clean

all: build

build: $(BIN_DIR)
	go build -o $(BIN_DIR)/$(TARGET) main.go

$(BIN_DIR):
	mkdir -p $(BIN_DIR)

clean:
	rm -rf $(BIN_DIR)
