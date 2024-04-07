package main

import (
	"act-nexus-cache/act"
	"context"
	"fmt"
	"github.com/nektos/act/pkg/common"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

func main() {
	ctx := context.Background()

	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	userHomeDir := home

	var CacheHomeDir string
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		CacheHomeDir = v
	} else {
		CacheHomeDir = filepath.Join(userHomeDir, ".cache")
	}

	cacheServerAddr := common.GetOutboundIP().String()
	cacheServerPath := filepath.Join(CacheHomeDir, "actcache")
	var cacheServerPort uint16 = 9900

	handler, err := act.StartHandler(cacheServerPath, cacheServerAddr, cacheServerPort, common.Logger(ctx))
	if err == nil {
		fmt.Printf("%v\n", handler.ExternalURL())
	} else {
		fmt.Printf("Error %v", err)
	}

	// Prepare to catch signals
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigs
		// Signal received, initiate graceful shutdown
		fmt.Println("\nSignal received, shutting down...")
		handler.Close()
	}()

	handler.Serve()
	//defer handler.Close()
}
