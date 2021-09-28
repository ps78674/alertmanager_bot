PKG_NAME=alertmanager_bot

LD_FLAGS='-s -w'

TARGET_TAR_NAME=$(PKG_NAME)

ifdef PKG_VER
VERSION_STRING=$(PKG_VER)
endif

ifdef PKG_REL
VERSION_STRING:=$(VERSION_STRING).$(PKG_REL)
endif

ifdef VERSION_STRING
LD_FLAGS:='-s -w -X "main.versionString=$(VERSION_STRING)"'
TARGET_TAR_NAME:=$(TARGET_TAR_NAME)-$(VERSION_STRING)
endif

BUILD_DIR=./build
INSTDIR=/usr/local/bin

clean: 
	rm -rf $(BUILD_DIR) $(PKG_NAME)*.tar.gz
build: 
	mkdir $(BUILD_DIR)
	go build -ldflags=$(LD_FLAGS) -o $(BUILD_DIR)/$(PKG_NAME) ./src
tar: build
	tar cfz $(TARGET_TAR_NAME).tar.gz --transform 's,^,alertmanager_bot/,' --transform 's,$(BUILD_DIR),/,' $(BUILD_DIR)/$(PKG_NAME) default.tmpl config.yaml

.DEFAULT_GOAL = build
