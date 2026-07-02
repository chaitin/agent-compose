package health

import (
	"bufio"
	"context"
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v4"
	"github.com/samber/do/v2"
	"google.golang.org/protobuf/types/known/emptypb"

	"agent-compose/internal/config"
	healthv1 "agent-compose/proto/health/v1"
	"agent-compose/proto/health/v1/healthv1connect"
)

type Service struct {
	startedAt        time.Time
	config           *config.Config
	processMu        sync.Mutex
	lastProcessProbe processProbe
}

type rpcServer struct {
	service *Service
	healthv1connect.UnimplementedHealthServiceHandler
}

func NewService(di do.Injector) (*Service, error) {
	return &Service{
		startedAt: time.Now(),
		config:    do.MustInvoke[*config.Config](di),
	}, nil
}

func Setup(di do.Injector) {
	do.Provide(di, NewService)

	app := do.MustInvoke[*echo.Echo](di)
	service := do.MustInvoke[*Service](di)

	path, handler := healthv1connect.NewHealthServiceHandler(&rpcServer{service: service})
	app.Any(path+"*", echo.WrapHandler(handler))
}

func (s *rpcServer) Status(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[healthv1.HealthStatusResponse], error) {
	_ = ctx
	_ = req

	return connect.NewResponse(s.service.snapshot()), nil
}

func (s *rpcServer) WatchStatus(ctx context.Context, req *connect.Request[emptypb.Empty], stream *connect.ServerStream[healthv1.HealthStatusResponse]) error {
	_ = req

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	if err := stream.Send(s.service.snapshot()); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := stream.Send(s.service.snapshot()); err != nil {
				return err
			}
		}
	}
}

func (s *Service) snapshot() *healthv1.HealthStatusResponse {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)

	now := time.Now()
	uptime := now.Sub(s.startedAt)

	return &healthv1.HealthStatusResponse{
		Version:       s.config.Version,
		CurrentTime:   now.Format(time.RFC3339Nano),
		StartedAt:     s.startedAt.Format(time.RFC3339Nano),
		UptimeSeconds: uint64(uptime / time.Second),
		GoVersion:     runtime.Version(),
		NumGoroutines: uint64(runtime.NumGoroutine()),
		BuildVersion:  s.config.Version,
		Memory: &healthv1.MemoryUsage{
			Alloc:      stats.Alloc,
			TotalAlloc: stats.TotalAlloc,
			Sys:        stats.Sys,
			NumGc:      uint32(stats.NumGC),
			HeapAlloc:  stats.HeapAlloc,
			HeapInuse:  stats.HeapInuse,
			HeapSys:    stats.HeapSys,
			HeapIdle:   stats.HeapIdle,
			StackInuse: stats.StackInuse,
			StackSys:   stats.StackSys,
		},
		Process: s.processUsage(now),
	}
}

type processProbe struct {
	at           time.Time
	cpuMillis    uint64
	userMillis   uint64
	systemMillis uint64
}

func (s *Service) processUsage(now time.Time) *healthv1.ProcessUsage {
	userMillis, systemMillis := processCPUMillis()
	totalMillis := userMillis + systemMillis

	s.processMu.Lock()
	cpuPercent := 0.0
	if !s.lastProcessProbe.at.IsZero() {
		elapsedMillis := float64(now.Sub(s.lastProcessProbe.at).Milliseconds())
		cpuDelta := float64(totalMillis - s.lastProcessProbe.cpuMillis)
		if elapsedMillis > 0 {
			cpuPercent = cpuDelta / elapsedMillis * 100
		}
	}
	s.lastProcessProbe = processProbe{
		at:           now,
		cpuMillis:    totalMillis,
		userMillis:   userMillis,
		systemMillis: systemMillis,
	}
	s.processMu.Unlock()

	ioStats := readProcessIO()

	return &healthv1.ProcessUsage{
		CpuPercent:      cpuPercent,
		CpuUserMillis:   userMillis,
		CpuSystemMillis: systemMillis,
		RssBytes:        processRSSBytes(),
		ReadBytes:       ioStats.readBytes,
		WriteBytes:      ioStats.writeBytes,
		ReadOps:         ioStats.readOps,
		WriteOps:        ioStats.writeOps,
	}
}

func processCPUMillis() (uint64, uint64) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, 0
	}
	userMillis := uint64(usage.Utime.Sec)*1000 + uint64(usage.Utime.Usec)/1000
	systemMillis := uint64(usage.Stime.Sec)*1000 + uint64(usage.Stime.Usec)/1000
	return userMillis, systemMillis
}

func processRSSBytes() uint64 {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return uint64(usage.Maxrss)
	}
	return uint64(usage.Maxrss) * 1024
}

type processIOStats struct {
	readBytes  uint64
	writeBytes uint64
	readOps    uint64
	writeOps   uint64
}

func readProcessIO() processIOStats {
	file, err := os.Open("/proc/self/io")
	if err != nil {
		return processIOFromRusage()
	}
	defer func() { _ = file.Close() }()

	var stats processIOStats
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), ":")
		if !ok {
			continue
		}
		parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "rchar":
			stats.readBytes = parsed
		case "wchar":
			stats.writeBytes = parsed
		case "read_bytes":
			if stats.readBytes == 0 {
				stats.readBytes = parsed
			}
		case "write_bytes":
			if stats.writeBytes == 0 {
				stats.writeBytes = parsed
			}
		case "syscr":
			stats.readOps = parsed
		case "syscw":
			stats.writeOps = parsed
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return processIOStats{}
	}
	return stats
}

func processIOFromRusage() processIOStats {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return processIOStats{}
	}
	readOps := uint64(maxInt64(usage.Inblock, 0))
	writeOps := uint64(maxInt64(usage.Oublock, 0))
	return processIOStats{
		readBytes:  readOps * 512,
		writeBytes: writeOps * 512,
		readOps:    readOps,
		writeOps:   writeOps,
	}
}

func maxInt64(value, minimum int64) int64 {
	if value < minimum {
		return minimum
	}
	return value
}
