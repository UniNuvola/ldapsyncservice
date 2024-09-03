EXE=ldapsyncservice

.PHONY: build push clean


$(EXE): cmd/*.go
	cd cmd/ && \
	CGO_ENABLED=0 go build && \
	mv cmd ../ldapsyncservice

build: $(EXE)
	docker build -t harbor1.fisgeo.unipg.it/uninuvola/ldapsyncservice .

push: build
	docker push harbor1.fisgeo.unipg.it/uninuvola/ldapsyncservice:latest 

clean:
	rm -f ldapsyncservice
