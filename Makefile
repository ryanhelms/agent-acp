.PHONY: setup generate check check-generated test

setup:
	npm ci
	npm run setup

generate:
	npm run generate

check-generated:
	npm run check:generated

test:
	cd sdks/go && go test ./...
	npm test --workspace sdks/typescript
	PYTHONPATH=sdks/python .tools/venv/bin/python -m unittest discover -s sdks/python/tests

check: check-generated test
