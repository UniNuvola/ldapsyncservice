#!/bin/bash

cd ../cmd
CGO_ENABLED=0 go build
cd -
mv ../cmd/cmd ldapsyncservice

docker build -t harbor1.fisgeo.unipg.it/uninuvola/ldapsyncservice .

rm -f ldapsyncservice
