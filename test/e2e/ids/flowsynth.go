package ids

import (
	"context"
	"fmt"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"github.com/tigera/flowsynth/pkg/scheduler"
	"github.com/tigera/flowsynth/pkg/synthesizer"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"os"
	"os/exec"
)

func genNodeNames(n int) []string {
	var nodes []string

	for i := 0; i < n; i++ {
		nodes = append(nodes, fmt.Sprintf("node%02d", i))
	}

	return nodes
}

func RunFlowSynth(ctx context.Context, config TestConfig) {
	logrus.SetLevel(logrus.ErrorLevel)
	nodes := genNodeNames(config.NumNodes)
	s := scheduler.NewCIDRScheduler(nodes, *config.PodNetwork)
	sy := synthesizer.NewSynthesizer(nodes)
	defer sy.StopOutputs()

	for _, aCfg := range config.Apps {
		a := aCfg.New(s)
		sy.RegisterApp(a)
	}

	for _, oCfg := range config.Outs {
		o := oCfg.New()
		sy.RegisterOutput(o)
		o.Start(ctx)
	}

	sy.Synthesize(config.StartTime, config.EndTime)
}

func RunFlowSynthBin(ctx context.Context, config TestConfig) {
	configFile, err := ioutil.TempFile("", "config-*.yaml")
	Expect(err).NotTo(HaveOccurred())
	defer func() {
		err := os.Remove(configFile.Name())
		Expect(err).NotTo(HaveOccurred())
	}()

	b, err := yaml.Marshal(&config)
	Expect(err).NotTo(HaveOccurred())

	_, err = configFile.Write(b)
	Expect(err).NotTo(HaveOccurred())

	err = configFile.Close()
	Expect(err).NotTo(HaveOccurred())

	cat := exec.CommandContext(ctx, "cat", configFile.Name())
	cat.Stdin = nil
	cat.Stderr = os.Stderr
	cat.Stdout = os.Stdout
	err = cat.Run()
	Expect(err).NotTo(HaveOccurred())

	cmd := exec.CommandContext(ctx, "flowsynth", "-config", configFile.Name())
	cmd.Stdin = nil
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	err = cmd.Run()
	Expect(err).NotTo(HaveOccurred())
}
