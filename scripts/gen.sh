#!/bin/bash
# Regenerates the plugin protocol code from proto/:
#   gen/                                       Go (buf + protoc-gen-go, protoc-gen-go-grpc)
#   internal/scaffold/python/nutng_plugin/_gen Python SDK (grpcio-tools)
#
# Needs Go, and a Python with grpcio-tools (PYTHON=..., default python3).
# Run it after changing proto/; it also refreshes the SDK copy in
# examples/plugins/python-driver.
set -euo pipefail
cd "$(dirname "$0")/.."
PYTHON="${PYTHON:-python3}"

go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
PATH="$(go env GOPATH)/bin:$PATH" go run github.com/bufbuild/buf/cmd/buf@v1.50.0 generate

out=internal/scaffold/python/nutng_plugin/_gen
rm -rf "$out"
mkdir -p "$out"
wk="$("$PYTHON" -c 'import grpc_tools, os; print(os.path.join(os.path.dirname(grpc_tools.__file__), "_proto"))')"
"$PYTHON" -m grpc_tools.protoc -I proto -I "$wk" --python_out="$out" --grpc_python_out="$out" proto/nutng/v1/*.proto
touch "$out/nutng/__init__.py" "$out/nutng/v1/__init__.py"
rm -rf examples/plugins/python-driver/nutng_plugin
cp -r internal/scaffold/python/nutng_plugin examples/plugins/python-driver/nutng_plugin
echo "generated"
