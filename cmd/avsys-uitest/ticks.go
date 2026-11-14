package main

import (
	"context"
	"fmt"
	"math"
	"time"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// gameCommand answers the commands the panel runs on a server's game to learn
// how fast it ticks, in the words of the software each server runs: Paper's
// tps and mspt on Paper, the game's tick query on 1.20.3 and later, and an
// unknown command on older versions, which say nothing of their ticks. The
// game is busy now and then: a tick takes longer than it may, and the rate
// drops below 20.
func (m *metricsStandIn) gameCommand(_ context.Context, mc *avsysv1.MinecraftServer, command string) (string, error) {
	at := m.now()
	// Mostly well under the 50 ms a tick may take, past it at the peaks.
	mspt := 6 + 60*math.Pow(wave(at, 40*time.Minute, 0.3), 8)
	tps := math.Min(20, 1000/mspt)
	paper := mc.Spec.Loader == avsysv1.LoaderPaper || mc.Spec.Loader == avsysv1.LoaderPurpur
	switch {
	case paper && command == "tps":
		return fmt.Sprintf("§6TPS from last 1m, 5m, 15m: §a%.2f§r, §a19.97§r, §a19.99\n", tps), nil
	case paper && command == "mspt":
		return fmt.Sprintf("§6Server tick times §e(§7avg§e/§7min§e/§7max§e)§6 from last 5s§7,§6 10s§7,§6 1m§e:\n§6◴ §a%.1f§7/§a0.2§7/§a%.1f§e, §a%.1f§7/§a0.2§7/§a%.1f§e, §a%.1f§7/§a0.2§7/§c%.1f\n", mspt, mspt*2, mspt, mspt*2, mspt, mspt*3), nil
	case command == "tick query" && mc.Spec.Version != "1.20.1":
		return fmt.Sprintf("The game is running normallyTarget tick rate: 20.0 per second.\nAverage time per tick: %.1fms (Target: 50.0ms)Percentiles: P50: %.1fms P95: %.1fms P99: %.1fms, sample: 100", mspt, mspt, mspt*1.5, mspt*2), nil
	}
	return "Unknown or incomplete command, see below for error" + command + "<--[HERE]", nil
}
