.PHONY: help c plugins main all rb exec run clean package install push compress \
        test-unit test-integration test-e2e test-coverage test-all-safe test-race \
        test-hardware test-report test-report-hardware test-report-full

APP_NAME := jamun
APP_DIR  := /mnt/app/jamun
ETHERLAB_INC := /opt/etherlab/include
ETHERLAB_LIB := /opt/etherlab/lib

CGO_CFLAGS  := -I$(PWD) -I$(ETHERLAB_INC)
CGO_LDFLAGS := -L$(PWD) -L$(APP_DIR) -L$(ETHERLAB_LIB) -Wl,-rpath,$(APP_DIR) -Wl,-rpath,$(PWD)

help: Makefile
	@echo " Choose a command to run :"
	@sed -n 's/^##//p' $< | column -t -s ':' | sed -e 's/^/ /'

## c: build EtherCAT C abstraction shared library
c:
	gcc -o libethercatinterface.so \
		-Wall -g -shared -fPIC ethercatinterface.c \
		-I$(ETHERLAB_INC) \
		$(ETHERLAB_LIB)/libethercat.a
	chmod 755 ./libethercatinterface.so

## plugins: build all command plugins
plugins:
	go build -ldflags="-s -w" -buildmode=plugin -o commands/g0.so commands/g0/g0.go
	go build -ldflags="-s -w" -buildmode=plugin -o commands/g01.so commands/g01/g01.go
	go build -ldflags="-s -w" -buildmode=plugin -o commands/divide360.so commands/divide360/divide360.go
	go build -ldflags="-s -w" -buildmode=plugin -o commands/divide360EnableDisable.so commands/divide360EnableDisable/divide360EnableDisable.go
	go build -ldflags="-s -w" -buildmode=plugin -o commands/rpm.so commands/rpm/rpm.go
	go build -ldflags="-s -w" -buildmode=plugin -o commands/moveRotary.so commands/moveRotary/moveRotary.go
	go build -ldflags="-s -w" -buildmode=plugin -o commands/loopStart.so commands/loopStart/loopStart.go
	go build -ldflags="-s -w" -buildmode=plugin -o commands/loopEnd.so commands/loopEnd/loopEnd.go
	go build -ldflags="-s -w" -buildmode=plugin -o commands/invalidCommand.so commands/invalidCommand/invalidCommand.go
	go build -ldflags="-s -w" -buildmode=plugin -o commands/g68.so commands/g68/g68.go
	go build -ldflags="-s -w" -buildmode=plugin -o commands/g69.so commands/g69/g69.go
	go build -ldflags="-s -w" -buildmode=plugin -o commands/g90.so commands/g90/g90.go
	go build -ldflags="-s -w" -buildmode=plugin -o commands/g91.so commands/g91/g91.go
	go build -ldflags="-s -w" -buildmode=plugin -o commands/delay.so commands/delay/delay.go
	go build -ldflags="-s -w" -buildmode=plugin -o commands/m30.so commands/m30/m30.go
	go build -ldflags="-s -w" -buildmode=plugin -o commands/m99.so commands/m99/m99.go
	go build -ldflags="-s -w" -buildmode=plugin -o commands/g17.so commands/g17/g17.go
	go build -ldflags="-s -w" -buildmode=plugin -o commands/workoffset.so commands/workoffset/workoffset.go

## main: build main.go with correct cgo flags and runtime library path
main: c
	CGO_ENABLED=1 \
	CGO_CFLAGS="$(CGO_CFLAGS)" \
	CGO_LDFLAGS="$(CGO_LDFLAGS) -lethercatinterface" \
	go build -ldflags="-s -w -X 'main.BuildTime=$$(date)'" -o $(APP_NAME) main.go
	chmod 755 ./$(APP_NAME)

## all: build C wrapper, plugins, and main app
all: c plugins main

## exec: stop service and execute compiled app
exec:
	sudo systemctl stop jamun || true
	sleep 2
	sudo env LD_LIBRARY_PATH=$(PWD):$(APP_DIR):$(ETHERLAB_LIB) GOGC=500 chrt -f 80 ./$(APP_NAME) || true
	@if [ -f /mnt/app/jamun/updates/current_update ] || [ -f /mnt/app/jamun/updates/pending_rollback ]; then \
		echo ">>> Pending OTA/Rollback detected - running pre_start_ota.sh..."; \
		sudo bash /mnt/app/jamun/scripts/pre_start_ota.sh; \
		echo ">>> Done. Run make exec again to start jamun on updated files."; \
	fi

## run: run main.go directly
run:
	CGO_ENABLED=1 \
	CGO_CFLAGS="$(CGO_CFLAGS)" \
	CGO_LDFLAGS="$(CGO_LDFLAGS) -lethercatinterface" \
	go run main.go

## rb: rebuild and execute
rb: main exec

## clean: clean binary, plugins, release folder
clean:
	rm -f ./$(APP_NAME)
	rm -f ./commands/*.so
	rm -f ./libethercatinterface.so
	rm -rf ./release

## test-unit: run unit tests
test-unit:
	go test -tags=unit ./motordriver/... ./helper/... ./configparser/... ./datatypes/... ./executors/... ./channels ./settings ./clientcommunication -count=1 -v

## test-integration: run integration tests with fakes only
test-integration:
	go test -tags=integration ./executors ./configparser ./restapi ./commands/moveRotary ./commands/g90 ./commands/g91 ./commands/m30 ./commands/m99 ./commands/g68 ./commands/g69 ./commands/g01 ./commands/loopStart ./commands/loopEnd ./commands/workoffset ./commands/invalidCommand ./commands/divide360 ./commands/g0 ./commands/g17 ./commands/rpm ./commands/delay ./commands/divide360EnableDisable -count=1 -v

## test-e2e: run e2e tests against live jamun app
test-e2e:
	go test -tags=e2e -v -timeout 120s ./e2e/...

## test-coverage: run safe coverage reports
test-coverage:
	go test -tags=unit ./motordriver/... ./helper/... ./configparser/... ./datatypes/... ./executors/... ./settings/... ./channels ./clientcommunication -coverprofile=coverage-unit.out
	go test -tags=integration ./executors ./configparser ./restapi ./settings ./commands/moveRotary ./commands/g90 ./commands/g91 ./commands/m30 ./commands/m99 ./commands/g68 ./commands/g69 ./commands/g01 ./commands/loopStart ./commands/loopEnd ./commands/workoffset ./commands/invalidCommand ./commands/divide360 ./commands/g0 ./commands/g17 ./commands/rpm ./commands/delay ./commands/divide360EnableDisable -coverprofile=coverage-integration.out
	@echo "Coverage files written: coverage-unit.out coverage-integration.out"
	@echo "Open with: go tool cover -html=coverage-unit.out"

## test-all-safe: run safe tests
test-all-safe: test-unit test-integration test-race

## test-race: run race detector
test-race:
	@if go test -race -tags=unit ./datatypes/... -count=1 -run TestReset_ClearsAllTransientState 2>&1 | grep -q "unsupported VMA"; then \
		echo "SKIP test-race: ThreadSanitizer unsupported on this kernel"; \
	else \
		go test -race -tags=unit ./helper/... ./configparser/... ./datatypes/... ./executors/... -count=1 && \
		go test -race -tags=integration ./executors ./configparser ./restapi ./commands/moveRotary ./commands/g90 ./commands/g91 ./commands/m30 ./commands/m99 ./commands/g68 ./commands/g69 ./commands/g01 ./commands/loopStart ./commands/loopEnd ./commands/workoffset ./commands/invalidCommand ./commands/divide360 -count=1; \
	fi

## test-hardware: run read-only hardware smoke tests
test-hardware:
	@./scripts/test-hardware-readonly.sh

## test-report: generate safe test report
test-report:
	@bash scripts/test-all-report.sh safe

## test-report-hardware: generate hardware test report
test-report-hardware:
	@bash scripts/test-all-report.sh hardware-motion

## test-report-full: generate full test report
test-report-full:
	@bash scripts/test-all-report.sh full

## package: build and package release. Usage: make package VERSION=1.0.33.0
package: all
	$(if $(VERSION),,$(error Version should not be empty, provide VERSION=1.0.0))
	$(eval fileName := jamun_v$(VERSION))
	$(eval dir_path := ./release/$(fileName))
	mkdir -p "./release"
	mkdir -p "/home/pi/ftp"
	mkdir -p "$(dir_path)"

	cp ./jamun                   "$(dir_path)/jamun"
	cp ./ethercatinterface.h     "$(dir_path)/ethercatinterface.h"
	cp ./ethercatinterface.c     "$(dir_path)/ethercatinterface.c"
	cp ./ethercatinterface.o     "$(dir_path)/ethercatinterface.o"
	cp ./libethercatinterface.so "$(dir_path)/libethercatinterface.so"
	echo "$(VERSION)" > "$(dir_path)/version.txt"

	mkdir -p "$(dir_path)/commands"
	cp ./commands/*.so "$(dir_path)/commands/"

	mkdir -p "$(dir_path)/configs"
	cp ./configs/M700.yml "$(dir_path)/configs/"
	cp ./configs/a6minas.yml "$(dir_path)/configs/"
	cp ./configs/device-configuration.yml "$(dir_path)/configs/"
	cp ./configs/error_definition.txt "$(dir_path)/configs/"
	cp ./configs/execution.yml "$(dir_path)/configs/"
	cp ./configs/envconfig.yaml "$(dir_path)/configs/"
	cp ./configs/code.json "$(dir_path)/configs/"
	cp ./configs/faq.json "$(dir_path)/configs/"
	cp ./configs/support.json "$(dir_path)/configs/"
	@if [ -f ./configs/delta_asda2e.yml ]; then cp ./configs/delta_asda2e.yml "$(dir_path)/configs/"; fi

	mkdir -p "$(dir_path)/scripts"
	cp ./scripts/apply_ota.sh "$(dir_path)/scripts/"
	cp ./scripts/pre_start_ota.sh "$(dir_path)/scripts/"
	cp ./scripts/setup_permissions.sh "$(dir_path)/scripts/"
	cp ./scripts/start.sh "$(dir_path)/scripts/"
	cp ./scripts/hotspot.sh "$(dir_path)/scripts/"
	cp ./scripts/tunnel.sh "$(dir_path)/scripts/"
	cp ./scripts/wifi.sh "$(dir_path)/scripts/"
	cp ./scripts/rollback.sh "$(dir_path)/scripts/"

	mkdir -p "$(dir_path)/www_v2"
	cp -r ./www_v2/. "$(dir_path)/www_v2/"

	find "$(dir_path)" -type d -exec chmod 755 {} +
	find "$(dir_path)" -type f -exec chmod 644 {} +
	chmod 755 "$(dir_path)/jamun" "$(dir_path)/scripts/"*.sh
	du -sh "$(dir_path)"
	cd ./release && tar -czf "$(fileName).tar.gz" "$(fileName)"
	cd ./release && sha256sum "$(fileName).tar.gz" > "$(fileName).tar.gz.sha256"
	rm -rf "$(dir_path)"
	cp "./release/$(fileName).tar.gz" /home/pi/ftp/
	cp "./release/$(fileName).tar.gz.sha256" /home/pi/ftp/

## install: copy files to Pi. Usage: make install PI=pi@192.168.1.100
install:
	$(if $(PI),,$(error Provide Pi address e.g. make install PI=pi@192.168.1.100))
	@echo ">>> Copying files to $(PI)..."
	ssh $(PI) "sudo mkdir -p /mnt/app/jamun/scripts"
	scp ./scripts/*.sh $(PI):/tmp/
	ssh $(PI) "sudo cp /tmp/*.sh /mnt/app/jamun/scripts/ && sudo chmod 750 /mnt/app/jamun/scripts/*.sh"
	scp ./jamun $(PI):/tmp/jamun
	ssh $(PI) "sudo cp /tmp/jamun /mnt/app/jamun/jamun && sudo chmod 755 /mnt/app/jamun/jamun"
	scp ./libethercatinterface.so $(PI):/tmp/libethercatinterface.so
	ssh $(PI) "sudo cp /tmp/libethercatinterface.so /mnt/app/jamun/libethercatinterface.so && sudo chmod 644 /mnt/app/jamun/libethercatinterface.so"
	@echo ">>> Running setup_permissions.sh on $(PI)..."
	ssh $(PI) "sudo bash /mnt/app/jamun/scripts/setup_permissions.sh"
	@echo ">>> Done. $(PI) is ready for OTA."

## push: push release to release server
push:
	$(if $(VERSION),,$(error Version should not be empty, provide VERSION=1.0.0))
	~/gosrc/src/release-cli/release-cli release -z /home/pi/gosrc/src/EtherCAT/release/jamun_v$(VERSION).tar.gz -p ebaf0856-606e-11eb-95b7-00155da1e406 -v \"$(VERSION)\" -c "v1"

## compress: compress binary and plugins
compress:
	chmod +x ./commands/*.so
	upx -9 -k ./commands/*.so
	upx -9 -k ./jamun
	rm -f ./commands/*.so~
	rm -f ./*.~
