package evictionmanager

import (
	"time"

	"eviction-agent/pkg/types"
	"eviction-agent/pkg/evictionclient"
	"eviction-agent/pkg/condition"
	"eviction-agent/pkg/log"
)

const (
	// updatePeriod is the period
	taintUpdatePeriod = 10 * time.Second
)

type EvictionManager interface {
	Run() error
}

type evictionManager struct {
	client              evictionclient.Client
	conditionManager    condition.ConditionManager
	evictChan           chan string
	nodeTaint           types.NodeTaintInfo
	unTaintGracePeriod  time.Duration
	lastTaintDiskIOTime time.Time
	lastTaintNetIOTime  time.Time
	lastTaintCPUTime    time.Time
	lastTaintMemTime    time.Time
}

// NewEvictionManager creates the eviction manager.
func NewEvictionManager(client evictionclient.Client, configFile string) EvictionManager {
	return &evictionManager{
		client:           client,
		conditionManager: condition.NewConditionManager(client, configFile),
		evictChan:        make(chan string, 1),
		nodeTaint:        types.NodeTaintInfo{
			DiskIO:    false,
			NetworkIO: false,
			CPU:       false,
			Memory:    false,
		},
	}
}

// Run starts the eviction manager
func (e *evictionManager) Run() error {
	// Start condition manager
	// get and update node condition and pod condition
	err := e.conditionManager.Start()
	if err != nil {
		return err
	}

	// Taint process
	go e.taintProcess()

	// Main run loop waiting on evicting request
	for {
		// wait for evict event
		select {
		case evictType := <-e.evictChan:
			log.Infof("evict pod because %s is not available", evictType)
		    e.evictOnePod(evictType)
		}
	}
	return nil
}

// evictOnePod call client to evict pod
func (e *evictionManager) evictOnePod(evictType string) {
	podToEvict, isEvict, priority, err:= e.conditionManager.ChooseOnePodToEvict(evictType)
	if err != nil {
		log.Errorf("evictOnePod choose one pod to evict error: %v", err)
		return
	}
	log.Infof("Get pod: %v to evict.\n", podToEvict.Name)

	if isEvict {
		err = e.client.EvictOnePod(podToEvict)
	} else {
		err = e.client.LabelPod(podToEvict, priority, "Add")
	}
	log.Infof("Evict pod : %v", err)
	return
}

func (e *evictionManager) taintProcess() {
	// taint process cycle
	var err error
	for {
		// wait for some second
		time.Sleep(taintUpdatePeriod)
		unTaintPeriod := e.conditionManager.GetUnTaintGracePeriod()
		// get taint condition
		e.nodeTaint, err = e.client.GetTaintConditions()
		if err != nil {
			log.Errorf("get taint condition error: %v", err)
			continue
		}

		// get node condition
		condition := e.conditionManager.GetNodeCondition()

		// node is in good condition currently
		if condition.NetworkRxAvailabel  && condition.NetworkTxAvailabel && condition.DiskIOAvailable &&
			condition.CPUAvailable && condition.MemoryAvailable &&
			!e.nodeTaint.DiskIO && !e.nodeTaint.NetworkIO && !e.nodeTaint.CPU && !e.nodeTaint.Memory {
			// node is in good condition, there is no need to taint or un-taint
			// there is no need to evict any pod either
			// only need to clear all annotations on pods
			e.client.ClearAllEvictLabels()
			continue
		}

		isEvicted := false
		// CPU condition process
		if condition.CPUAvailable {
			if e.nodeTaint.CPU {
				// node is tainted CPU busy
				// TODO: wait taintGraceTime
				duration := time.Now().Sub(e.lastTaintCPUTime)
				log.Infof("last taint duration: %v", duration)
				if duration.Minutes() > unTaintPeriod.Minutes() {
					err = e.client.SetTaintConditions(types.CPUBusy, "UnTaint")
					log.Infof("Untaint node %s", types.CPUBusy)
					if err != nil {
						log.Errorf("untaint node %s error: %v", types.CPUBusy, err)
					}
					// TODO: clear annotations
				}
			}
		} else {
			// node is in CPU busy
			// update taint time
			e.lastTaintCPUTime = time.Now()
			if !e.nodeTaint.CPU {
				// taint node, evict pod
				log.Infof("taint node %s ", types.CPUBusy)
				err = e.client.SetTaintConditions(types.CPUBusy, "Taint")
				if err != nil {
					log.Errorf("add taint %s error: %v", types.CPUBusy, err)
				}
			}
			// evict one pod to reclaim resources
			if !isEvicted {
				isEvicted = true
				e.evictChan <- types.CPUBusy
			}
		}

		// Memory condition process
		if condition.MemoryAvailable {
			if e.nodeTaint.Memory {
				// node is tainted Memory busy
				// TODO: wait taintGraceTime
				duration := time.Now().Sub(e.lastTaintMemTime)
				log.Infof("last taint duration: %v\n", duration)
				if duration.Minutes() > unTaintPeriod.Minutes() {
					err = e.client.SetTaintConditions(types.MemBusy, "UnTaint")
					log.Infof("Untaint node %s", types.MemBusy)
					if err != nil {
						log.Errorf("untaint node %s error: %v", types.MemBusy, err)
					}
					// TODO: clear annotations
				}
			}
		} else {
			// node is in Memory busy
			// update taint time
			e.lastTaintMemTime = time.Now()
			if !e.nodeTaint.Memory {
				// taint node, evict pod
				log.Infof("taint node %s ", types.MemBusy)
				err = e.client.SetTaintConditions(types.MemBusy, "Taint")
				if err != nil {
					log.Errorf("add taint %s error: %v", types.MemBusy, err)
				}
			}
			// evict one pod to reclaim resources
			if !isEvicted {
				isEvicted = true
				e.evictChan <- types.MemBusy
			}
		}

		// DiskIO condition process
		if condition.DiskIOAvailable {
			if e.nodeTaint.DiskIO {
				// node is tainted DiskIO busy
				// TODO: wait taintGraceTime
				duration := time.Now().Sub(e.lastTaintDiskIOTime)
				log.Infof("last taint duration: %v", duration)
				if duration.Minutes() > unTaintPeriod.Minutes() {
					err = e.client.SetTaintConditions(types.DiskIO, "UnTaint")
					log.Infof("Untaint node %s", types.DiskIO)
					if err != nil {
						log.Errorf("untaint node %s error: %v", types.DiskIO, err)
					}
					// TODO: clear annotations
				}
			}
		} else {
			// node is in DiskIO busy
			// update taint time
			e.lastTaintDiskIOTime = time.Now()
			if !e.nodeTaint.DiskIO {
				// taint node, evict pod
				log.Infof("taint node %s ", types.DiskIO)
				err = e.client.SetTaintConditions(types.DiskIO, "Taint")
				if err != nil {
					log.Errorf("add taint %s error: %v", types.DiskIO, err)
				}
			}
			// evict one pod to reclaim resources
			if !isEvicted {
				isEvicted = true
				e.evictChan <- types.DiskIO
			}
		}

		// NetworkIO condition process
		if condition.NetworkRxAvailabel && condition.NetworkTxAvailabel {
			if e.nodeTaint.NetworkIO {
				duration := time.Now().Sub(e.lastTaintNetIOTime)
				log.Infof("last taint duration: %v", duration)
				if duration.Minutes() > unTaintPeriod.Minutes() {
					err = e.client.SetTaintConditions(types.NetworkIO, "UnTaint")
					if err != nil {
						log.Errorf("untaint node %s error: %v", types.NetworkIO, err)
					}
					// TODO: clear annotations
					log.Infof("untaint node %s", types.NetworkIO)
				}
			}
		} else {
			// node is in NetworkIO busy
			e.lastTaintNetIOTime = time.Now()
			if !e.nodeTaint.NetworkIO {
				log.Infof("taint node %s unavailable", types.NetworkIO)
				// taint node, evict pod
				err = e.client.SetTaintConditions(types.NetworkIO, "Taint")
				if err != nil {
					log.Errorf("add taint %s error: %v", types.NetworkIO, err)
				}
			}
			// evict one pod to reclaim resources
			if !isEvicted {
				isEvicted = true
				if !condition.NetworkTxAvailabel {
					e.evictChan <- types.NetworkRxBusy
				} else if !condition.NetworkTxAvailabel {
					e.evictChan <- types.NetworkTxBusy
				}

			}
		}
	}
}
