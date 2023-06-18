package monitor

import (
	"errors"
	"os"
	"sync"
	"time"

	"github.com/mu-box/yoke/config"
	"github.com/mu-box/yoke/state"
)

var (
	ClusterUnaviable = errors.New("none of the nodes in the cluster are available")
)

type (
	Looper interface {
		Loop(time.Duration) error
	}

	decider struct {
		sync.Mutex

		me        state.State
		other     state.State
		monitor   state.State
		performer Performer
	}
)

func NewDecider(me, other, monitor state.State, performer Performer) Looper {
	decider := decider{
		me:        me,
		other:     other,
		monitor:   monitor,
		performer: performer,
	}
	for {
		// Really we only have to wait for a quorum, 2 out of 3 will allow everything to be ok.
		// But in certain conditions, this node was a backup that was down, and the current active
		// if offline, we need to wait for all 3 nodes.
		// So really we are going to wait for all 3 nodes to make it simple
		// me is already Ready. no need to call it
		config.Log.Info("waiting for cluster to be ready")
		other.Ready()
		monitor.Ready()
		config.Log.Info("cluster is ready")

		err := decider.reCheck()
		switch err {
		case ClusterUnaviable: // we try again.
		case nil: // the cluster was successfully rechecked
			return decider
		default:
			config.Log.Fatal("Another kind of error occured: %v", err)
			os.Exit(1)
		}
	}
}

// this is the main loop for monitoring the cluster and making any changes needed to
// reflect changes in remote nodes in the cluster
func (decider decider) Loop(check time.Duration) error {
	timer := time.Tick(check)
	for range timer {
		err := decider.reCheck()
		switch {
		case err == ClusterUnaviable:
		case err != nil:
			return err
		default:
		}
	}
	return nil
}

// this is used to move a active node to a backup node
func (decider decider) Demote() {
	decider.Lock()
	defer decider.Unlock()

	decider.performer.TransitionToBackup()
}

// this is used to move a backup node to an active node
func (decider decider) Promote() {
	decider.Lock()
	defer decider.Unlock()

	decider.performer.TransitionToActive()
}

// Checks the other node in the cluster, falling back to bouncing the check off of the monitor,
// to see if the states between this node and the remote node match up
func (decider decider) reCheck() error {
	decider.Lock()
	defer decider.Unlock()

	var otherDBRole string
	var err error
	config.Log.Info("checking other role")
	otherDBRole, err = decider.other.GetDBRole()
	if err != nil {
		config.Log.Info("checking other role (bounce)")
		address := decider.other.Location()
		otherDBRole, err = decider.monitor.Bounce(address).GetDBRole()
		if err != nil {
			// this node can't talk to the other member of the cluster or the monitor
			// if this node is not in single mode it needs to shut off
			if role, err := decider.me.GetDBRole(); role != "single" || err != nil {
				config.Log.Info("stopping, no one here")
				decider.performer.Stop()
				return ClusterUnaviable
			}
			return nil
		}
	}

	config.Log.Info("other node is '%v'", otherDBRole)

	// we need to handle multiple possible states that the remote node is in
	switch otherDBRole {
	case "single":
		fallthrough
	case "active":
		decider.performer.TransitionToBackup()
	case "dead":
		DBrole, err := decider.me.GetDBRole()
		if err != nil {
			return err
		}
		if DBrole == "backup" {
			// if this node is not synced up to the previous master, then we must wait for the other node to
			// come online
			hasSynced, err := decider.me.HasSynced()
			if err != nil {
				return err
			}
			if !hasSynced {
				decider.performer.Stop()
				return ClusterUnaviable
			}
		}

		decider.performer.TransitionToSingle()
	case "initialized":
		role, err := decider.me.GetRole()
		if err != nil {
			return err
		}
		switch role {
		case "primary":
			decider.performer.TransitionToActive()
		case "secondary":
			decider.performer.TransitionToBackup()
		}
	case "backup":
		decider.performer.TransitionToActive()
	}
	return nil
}
