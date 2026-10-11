package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/swysgh/mill-box/cmd/internal/build_shared"
	"github.com/swysgh/mill-box/log"

	_ "github.com/sagernet/gomobile"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/rw"
	"github.com/sagernet/sing/common/shell"
)

var (
	debugEnabled bool
	target       string
	platform     string
	// withTailscale bool
)

func init() {
	flag.BoolVar(&debugEnabled, "debug", false, "enable debug")
	flag.StringVar(&target, "target", "android", "target platform")
	flag.StringVar(&platform, "platform", "", "specify platform")
	// flag.BoolVar(&withTailscale, "with-tailscale", false, "build tailscale for iOS and tvOS")
}

func main() {
	flag.Parse()

	build_shared.FindMobile()

	switch target {
	case "android":
		buildAndroid()
	}
}

var (
	sharedFlags []string
	debugFlags  []string
	sharedTags  []string
	// memcTags    []string
	notMemcTags []string
	debugTags   []string
)

func init() {
	sharedFlags = append(sharedFlags, "-trimpath")
	sharedFlags = append(sharedFlags, "-buildvcs=false")
	currentTag, err := build_shared.ReadTag()
	if err != nil {
		currentTag = "unknown"
	}
	sharedFlags = append(sharedFlags, "-ldflags", build_shared.LinkerFlags(currentTag, false))
	debugFlags = append(debugFlags, "-ldflags", build_shared.LinkerFlags(currentTag, true))

	sharedTags = append(sharedTags, "with_quic", "with_wireguard", "with_utls", "with_naive_outbound", "with_clash_api", "with_openvpn", "with_openconnect", "badlinkname", "tfogo_checklinkname0")
	notMemcTags = append(notMemcTags, "with_low_memory")
	debugTags = append(debugTags, "debug")
}

type AndroidBuildConfig struct {
	AndroidAPI int
	OutputName string
	Tags       []string
}

func filterTags(tags []string, exclude ...string) []string {
	excludeMap := make(map[string]bool)
	for _, tag := range exclude {
		excludeMap[tag] = true
	}
	var result []string
	for _, tag := range tags {
		if !excludeMap[tag] {
			result = append(result, tag)
		}
	}
	return result
}

func checkJavaVersion() {
	var javaPath string
	javaHome := os.Getenv("JAVA_HOME")
	if javaHome == "" {
		javaPath = "java"
	} else {
		javaPath = filepath.Join(javaHome, "bin", "java")
	}

	javaVersion, err := shell.Exec(javaPath, "--version").ReadOutput()
	if err != nil {
		log.Fatal(E.Cause(err, "check java version"))
	}
	if !strings.Contains(javaVersion, "openjdk 17") {
		log.Fatal("java version should be openjdk 17")
	}
}

func getAndroidBindTarget() string {
	if platform != "" {
		return platform
	} else if debugEnabled {
		return "android/arm64"
	}
	return "android"
}

func buildAndroidVariant(config AndroidBuildConfig, bindTarget string) {
	args := []string{
		"bind",
		"-v",
		"-o", config.OutputName,
		"-target", bindTarget,
		"-androidapi", strconv.Itoa(config.AndroidAPI),
		"-javapkg=io.nekohasekai",
		"-libname=box",
	}

	if !debugEnabled {
		args = append(args, sharedFlags...)
	} else {
		args = append(args, debugFlags...)
	}

	args = append(args, "-tags", strings.Join(config.Tags, ","))
	args = append(args, "./experimental/libbox")

	command := exec.Command(build_shared.GoBinPath+"/gomobile", args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	err := command.Run()
	if err != nil {
		log.Fatal(err)
	}

	copyPath := filepath.Join("..", "sing-box-for-android", "app", "libs")
	if rw.IsDir(copyPath) {
		copyPath, _ = filepath.Abs(copyPath)
		err = rw.CopyFile(config.OutputName, filepath.Join(copyPath, config.OutputName))
		if err != nil {
			log.Fatal(err)
		}
		log.Info("copied ", config.OutputName, " to ", copyPath)
	}
}

func buildAndroid() {
	build_shared.FindSDK()
	checkJavaVersion()

	bindTarget := getAndroidBindTarget()

	// Build main variant (SDK 24)
	mainTags := append([]string{}, sharedTags...)
	// mainTags = append(mainTags, memcTags...)
	if debugEnabled {
		mainTags = append(mainTags, debugTags...)
	}
	buildAndroidVariant(AndroidBuildConfig{
		AndroidAPI: 24,
		OutputName: "libbox.aar",
		Tags:       mainTags,
	}, bindTarget)

	// Build legacy variant (SDK 21, no naive outbound)
	legacyTags := filterTags(sharedTags, "with_naive_outbound")
	// legacyTags = append(legacyTags, memcTags...)
	if debugEnabled {
		legacyTags = append(legacyTags, debugTags...)
	}
	buildAndroidVariant(AndroidBuildConfig{
		AndroidAPI: 21,
		OutputName: "libbox-legacy.aar",
		Tags:       legacyTags,
	}, bindTarget)
}
