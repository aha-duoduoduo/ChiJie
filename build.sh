#!/bin/bash
set -e

echo "Building frontend..."
cd web
npm ci
npm run build
cd ..

echo "Copying dist to internal/admin..."
rm -rf internal/admin/dist
cp -r web/dist internal/admin/dist

echo "Building Go binary..."
go build -tags with_utls -ldflags="-X chijie/internal/buildinfo.BuiltAt=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o chijie ./cmd/gateway/

echo "Done! Binary: ./chijie"
