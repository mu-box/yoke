package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	scribble "github.com/mu-box/golang-scribble"
	"github.com/mu-box/yoke/config"
	"github.com/mu-box/yoke/monitor"
	"github.com/mu-box/yoke/state"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Println("Missing required config file!")
		fmt.Println("Please, run yoke with configuration file as argument (e.g. $ yoke /etc/yoke/yoke.ini)")
		os.Exit(1)
	}
	config.Init(os.Args[1])

	config.ConfigurePGConf("0.0.0.0", config.Conf.PGPort)

	store, err := scribble.New(config.Conf.StatusDir, &scribble.Options{Logger: config.Log})
	if err != nil {
		config.Log.Fatal("Scribble did not setup correctly - ", err.Error())
		os.Exit(1)
	}

	location := fmt.Sprintf("%v:%d", config.Conf.AdvertiseIp, config.Conf.AdvertisePort)
	me, err := state.NewLocalState(config.Conf.Role, location, config.Conf.DataDir, store)
	if err != nil {
		config.Log.Fatal("Failed to set local state - ", err.Error())
		os.Exit(1)
	}

	me.ExposeRPCEndpoint("tcp", location)

	var other state.State
	var host string
	switch config.Conf.Role {
	case "primary":
		location := config.Conf.Secondary
		other = state.NewRemoteState("tcp", location, time.Second)
		host, _, err = net.SplitHostPort(location)
		if err != nil {
			config.Log.Fatal("Failed to split host:port for primary node - ", err.Error())
			os.Exit(1)
		}
	case "secondary":
		location := config.Conf.Primary
		other = state.NewRemoteState("tcp", location, time.Second)
		host, _, err = net.SplitHostPort(location)
		if err != nil {
			config.Log.Fatal("Failed to split host:port for secondary node - ", err.Error())
			os.Exit(1)
		}
	default:
		// nothing as the monitor does not need to monitor anything
		// the monitor just acts as a secondary mode of communication in network
		// splits
	}

	mon := state.NewRemoteState("tcp", config.Conf.Monitor, time.Second)

	var perform monitor.Performer
	finished := make(chan error)
	if other != nil {

		perform = monitor.NewPerformer(me, other, config.Conf)

		if err := perform.Initialize(); err != nil {
			config.Log.Fatal("Failed to initialize database - ", err.Error())
			os.Exit(1)
		}

		if err := config.ConfigureHBAConf(host); err != nil {
			config.Log.Fatal("Failed to configure pg_hba.conf file - ", err.Error())
			os.Exit(1)
		}

		if err := config.ConfigurePGConf("0.0.0.0", config.Conf.PGPort); err != nil {
			config.Log.Fatal("Failed to configure postgresql.conf file - ", err.Error())
			os.Exit(1)
		}

		if err := perform.Start(); err != nil {
			config.Log.Fatal("Failed to start postgres - ", err.Error())
			os.Exit(1)
		}

		go func() {
			decide := monitor.NewDecider(me, other, mon, perform)
			decide.Loop(time.Second * 2)
		}()

		go func() {
			err := perform.Loop()
			if err != nil {
				finished <- err
			}
			// how do I stop the decide loop?
			close(finished)
		}()
	}

	// signal Handle
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, os.Kill, syscall.SIGQUIT, syscall.SIGALRM)

	// Block until a signal is received.
	for {
		select {
		case err := <-finished:
			if err != nil {
				config.Log.Fatal("The performer is finished, something triggered a stop - ", err.Error())
				os.Exit(1)
			}
			config.Log.Info("the database was shut down")
			return
		case signal := <-signals:
			switch signal {
			case syscall.SIGINT, os.Kill, syscall.SIGQUIT, syscall.SIGTERM:
				config.Log.Info("shutting down")
				if perform != nil {
					// stop the database, then wait for it to be stopped
					config.Log.Info("shutting down the database")
					perform.Stop()
					perform = nil
					config.Log.Info("waiting for the database")
				} else {
					return
				}
			case syscall.SIGALRM:
				config.Log.Info("Printing Stack Trace")
				stacktrace := make([]byte, 8192)
				length := runtime.Stack(stacktrace, true)
				fmt.Println(string(stacktrace[:length]))
			}
		}
	}
}
