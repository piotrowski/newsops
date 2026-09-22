VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
PREFIX  ?= $(HOME)/.local
HARNESSES ?= .claude .opencode .cursor

.PHONY: build install skills test check sources clean

build:
	go build -ldflags "-X main.Version=$(VERSION) -X main.Commit=$(COMMIT)" -o newsops ./cmd/newsops

install: build
	mkdir -p $(PREFIX)/bin
	cp newsops $(PREFIX)/bin/

skills:
	mkdir -p .agents/skills/news-ops
	@for harness in $(HARNESSES); do \
		target=$$harness/skills/news-ops; \
		mkdir -p "$$target"; \
		rm -f "$$target/SKILL.md"; \
		ln -s ../../../.agents/skills/news-ops/SKILL.md "$$target/SKILL.md"; \
	done

test:
	go test ./...

check: build test skills
	@for f in $$(grep -o 'modes/[a-z_]*\.md' .agents/skills/news-ops/SKILL.md); do \
		test -f "$$f" || { echo "missing: $$f"; exit 1; }; \
	done
	@for harness in $(HARNESSES); do \
		target=$$harness/skills/news-ops/SKILL.md; \
		[ -L "$$target" ] || { echo "not a symlink: $$target"; exit 1; }; \
		[ -e "$$target" ] || { echo "broken symlink: $$target -> $$(readlink "$$target")"; exit 1; }; \
	done

# Network reachability of the feed list. Separate from check: feeds rot
# independently of the code, and a dead feed is not a build failure.
sources: build
	./newsops sources check --json

clean:
	rm -f newsops
