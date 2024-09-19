EXE=ldapsyncservice

.PHONY: build push clean


$(EXE): cmd/*.go
	cd cmd/ && \
	CGO_ENABLED=0 go build && \
	mv cmd ../ldapsyncservice


clean:
	rm -f ldapsyncservice
