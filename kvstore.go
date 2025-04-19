package main
import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb"
	"github.com/gorilla/mux"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"path/filepath"
)

type Printer struct {
	ID      string `json:"id"`
	Company string `json:"company"`
	Model   string `json:"model"`
}

type Filament struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Color                string `json:"color"`
	TotalWeightGrams     int    `json:"total_weight_in_grams"`
	RemainingWeightGrams int    `json:"remaining_weight_in_grams"`
}

type PrintJob struct {
	ID               string `json:"id"`
	PrinterID        string `json:"printer_id"`
	FilamentID       string `json:"filament_id"`
	FilePath         string `json:"filepath"`
	PrintWeightGrams int    `json:"print_weight_in_grams"`
	Status           string `json:"status"`
}

type Raft3DStore struct {
	sync.RWMutex
	raft      *raft.Raft
	Printers  map[string]Printer
	Filaments map[string]Filament
	PrintJobs map[string]PrintJob
}

var (
	store = &Raft3DStore{
		Printers:  make(map[string]Printer),
		Filaments: make(map[string]Filament),
		PrintJobs: make(map[string]PrintJob),
	}
	shutdownCh = make(chan struct{}) 
)


type Command struct {
	Op    string      `json:"op"`
	Value interface{} `json:"value"`
}

type FSM struct{}

func (f *FSM) Apply(logEntry *raft.Log) interface{} {
	var cmd Command
	if err := json.Unmarshal(logEntry.Data, &cmd); err != nil {
		log.Printf("[FSM] Unmarshal error: %v", err)
		return nil
	}

	store.Lock()
	defer store.Unlock()

	switch cmd.Op {
	case "add_printer":
		p, ok := cmd.Value.(map[string]interface{})
		if !ok {
			log.Printf("[FSM] Invalid printer data")
			return nil
		}
		printer := Printer{
			ID:      p["id"].(string),
			Company: p["company"].(string),
			Model:   p["model"].(string),
		}
		store.Printers[printer.ID] = printer

	case "add_filament":
		f, ok := cmd.Value.(map[string]interface{})
		if !ok {
			log.Printf("[FSM] Invalid filament data")
			return nil
		}
		filament := Filament{
			ID:                   f["id"].(string),
			Type:                 f["type"].(string),
			Color:                f["color"].(string),
			TotalWeightGrams:     int(f["total_weight_in_grams"].(float64)),
			RemainingWeightGrams: int(f["remaining_weight_in_grams"].(float64)),
		}
		store.Filaments[filament.ID] = filament

	case "add_print_job":
		j, ok := cmd.Value.(map[string]interface{})
		if !ok {
			log.Printf("[FSM] Invalid job data")
			return nil
		}
		job := PrintJob{
			ID:               j["id"].(string),
			PrinterID:        j["printer_id"].(string),
			FilamentID:       j["filament_id"].(string),
			FilePath:         j["filepath"].(string),
			PrintWeightGrams: int(j["print_weight_in_grams"].(float64)),
			Status:           j["status"].(string),
		}
		store.PrintJobs[job.ID] = job

	case "update_job_status":
		j, ok := cmd.Value.(map[string]interface{})
		if !ok {
			log.Printf("[FSM] Invalid status update data")
			return nil
		}
		id := j["id"].(string)
		newStatus := j["status"].(string)
		job, exists := store.PrintJobs[id]
		if !exists {
			return nil
		}
		if isValidTransition(job.Status, newStatus) {
			if job.Status == "Running" && newStatus == "Done" {
				if fil, exists := store.Filaments[job.FilamentID]; exists {
					fil.RemainingWeightGrams -= job.PrintWeightGrams
					store.Filaments[job.FilamentID] = fil
				}
			}
			job.Status = newStatus
			store.PrintJobs[id] = job
		}

	case "clear_store":
		store.Printers = make(map[string]Printer)
		store.Filaments = make(map[string]Filament)
		store.PrintJobs = make(map[string]PrintJob)
	}
	return nil
}

func isValidTransition(current, new string) bool {
	validTransitions := map[string][]string{
		"Queued":   {"Running", "Canceled"},
		"Running":  {"Done", "Canceled"},
		"Done":     {},
		"Canceled": {},
	}
	for _, valid := range validTransitions[current] {
		if valid == new {
			return true
		}
	}
	return false
}

func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	store.RLock()
	defer store.RUnlock()

	data := struct {
		Printers  map[string]Printer
		Filaments map[string]Filament
		PrintJobs map[string]PrintJob
	}{
		Printers:  store.Printers,
		Filaments: store.Filaments,
		PrintJobs: store.PrintJobs,
	}

	serialized, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	return &fsmSnapshot{state: serialized}, nil
}

func (f *FSM) Restore(rc io.ReadCloser) error {
	defer rc.Close()

	var data struct {
		Printers  map[string]Printer
		Filaments map[string]Filament
		PrintJobs map[string]PrintJob
	}

	if err := json.NewDecoder(rc).Decode(&data); err != nil {
		return err
	}

	store.Lock()
	defer store.Unlock()
	store.Printers = data.Printers
	store.Filaments = data.Filaments
	store.PrintJobs = data.PrintJobs
	return nil
}

type fsmSnapshot struct {
	state []byte
}

func (s *fsmSnapshot) Persist(sink raft.SnapshotSink) error {
	if _, err := sink.Write(s.state); err != nil {
		sink.Cancel()
		return err
	}
	return sink.Close()
}

func (s *fsmSnapshot) Release() {}


func applyCommand(op string, value interface{}) error {
	cmd := Command{Op: op, Value: value}
	data, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("marshal error: %v", err)
	}

	future := store.raft.Apply(data, 5*time.Second)
	if err := future.Error(); err != nil {
		return fmt.Errorf("raft apply error: %v", err)
	}
	return nil
}

func contains(slice []string, val string) bool {
	for _, s := range slice {
		if strings.EqualFold(s, val) {
			return true
		}
	}
	return false
}


func postPrinter(w http.ResponseWriter, r *http.Request) {
	var printer Printer
	if err := json.NewDecoder(r.Body).Decode(&printer); err != nil {
		http.Error(w, "Invalid printer data", http.StatusBadRequest)
		return
	}

	if err := applyCommand("add_printer", printer); err != nil {
		http.Error(w, "Failed to add printer", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func getPrinters(w http.ResponseWriter, r *http.Request) {
	store.RLock()
	defer store.RUnlock()
	if err := json.NewEncoder(w).Encode(store.Printers); err != nil {
		http.Error(w, "Failed to encode printers", http.StatusInternalServerError)
	}
}

func postFilament(w http.ResponseWriter, r *http.Request) {
	var filament Filament
	if err := json.NewDecoder(r.Body).Decode(&filament); err != nil {
		http.Error(w, "Invalid filament data", http.StatusBadRequest)
		return
	}

	if err := applyCommand("add_filament", filament); err != nil {
		http.Error(w, "Failed to add filament", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func getFilaments(w http.ResponseWriter, r *http.Request) {
	store.RLock()
	defer store.RUnlock()
	if err := json.NewEncoder(w).Encode(store.Filaments); err != nil {
		http.Error(w, "Failed to encode filaments", http.StatusInternalServerError)
	}
}

func postPrintJob(w http.ResponseWriter, r *http.Request) {
	var job PrintJob
	if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
		http.Error(w, "Invalid job data", http.StatusBadRequest)
		return
	}

	store.RLock()
	_, pExists := store.Printers[job.PrinterID]
	fil, fExists := store.Filaments[job.FilamentID]
	
	totalWeight := 0
	for _, j := range store.PrintJobs {
		if j.FilamentID == job.FilamentID && (j.Status == "Queued" || j.Status == "Running") {
			totalWeight += j.PrintWeightGrams
		}
	}
	store.RUnlock()

	if !pExists || !fExists {
		http.Error(w, "Printer or filament not found", http.StatusBadRequest)
		return
	}
	if job.PrintWeightGrams+totalWeight > fil.RemainingWeightGrams {
		http.Error(w, "Insufficient filament", http.StatusBadRequest)
		return
	}

	if err := applyCommand("add_print_job", job); err != nil {
		http.Error(w, "Failed to add print job", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func updateJobStatus(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]
	status := r.URL.Query().Get("status")
	if status == "" {
		http.Error(w, "Status parameter required", http.StatusBadRequest)
		return
	}

	store.RLock()
	job, exists := store.PrintJobs[id]
	store.RUnlock()

	if !exists {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}

	if !isValidTransition(job.Status, status) {
		http.Error(w, "Invalid status transition", http.StatusBadRequest)
		return
	}
	payload := map[string]interface{}{"id": id, "status": status}
	if err := applyCommand("update_job_status", payload); err != nil {
		http.Error(w, "Failed to update job status", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func getPrintJobs(w http.ResponseWriter, r *http.Request) {
	store.RLock()
	defer store.RUnlock()
	if err := json.NewEncoder(w).Encode(store.PrintJobs); err != nil {
		http.Error(w, "Failed to encode print jobs", http.StatusInternalServerError)
	}
}

func handleClearStore(w http.ResponseWriter, r *http.Request) {
	if err := applyCommand("clear_store", nil); err != nil {
		http.Error(w, "Failed to clear store", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Store cleared"))
}


func joinRaftCluster(joinAddr, raftAddr, nodeID string) error {
	url := fmt.Sprintf("http://%s/join?addr=%s&id=%s", joinAddr, raftAddr, nodeID)
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("join request failed: %v", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("join failed with status: %s", resp.Status)
	}
	return nil
}

func handleJoin(w http.ResponseWriter, r *http.Request) {
	peerAddr := r.URL.Query().Get("addr")
	peerID := r.URL.Query().Get("id")
	
	if peerAddr == "" || peerID == "" {
		http.Error(w, "addr and id parameters required", http.StatusBadRequest)
		return
	}

	log.Printf("[Cluster] Join request from %s at %s", peerID, peerAddr)
	
	future := store.raft.AddVoter(
		raft.ServerID(peerID),
		raft.ServerAddress(peerAddr),
		0,
		10*time.Second,
	)
	
	if err := future.Error(); err != nil {
		http.Error(w, fmt.Sprintf("failed to add voter: %v", err), http.StatusInternalServerError)
		return
	}
	
	w.WriteHeader(http.StatusOK)
}


func setupRaft(nodeID string, raftPort int, raftDBPath string) error {
	config := raft.DefaultConfig()
	config.LocalID = raft.ServerID(nodeID)
	
	config.HeartbeatTimeout = 300 * time.Millisecond
	config.ElectionTimeout = 2 * time.Second  // Reasonable election timeout
	config.LeaderLeaseTimeout = 250 * time.Millisecond  // Set less than HeartbeatTimeout
	config.CommitTimeout = 50 * time.Millisecond
	config.SnapshotInterval = 20 * time.Second
	config.SnapshotThreshold = 2
	config.LogOutput = os.Stderr

	notifyCh := make(chan bool, 1)
	config.NotifyCh = notifyCh

	go func() {
		for isLeader := range notifyCh {
			if isLeader {
				log.Printf("[Raft] I AM THE LEADER (%s)", nodeID)
			} else {
				log.Printf("[Raft] LOST LEADERSHIP (%s)", nodeID)
			}
		}
	}()
	go func() {
	    ticker := time.NewTicker(2 * time.Second)
	    for range ticker.C {
	        log.Printf("[Raft] Current State: %s", store.raft.State())
	    }
	}()


	addr, err := raft.NewTCPTransport(
		fmt.Sprintf("127.0.0.1:%d", raftPort),
		nil,
		3,
		200*time.Millisecond, 
		os.Stderr,
	)
	if err != nil {
		return fmt.Errorf("failed to create transport: %v", err)
	}

	logStore, err := raftboltdb.NewBoltStore(raftDBPath)
	if err != nil {
		return fmt.Errorf("failed to create log store: %v", err)
	}

	snapshots, err := raft.NewFileSnapshotStore(
		filepath.Dir(raftDBPath),
		3,
		os.Stderr,
	)
	if err != nil {
		return fmt.Errorf("failed to create snapshot store: %v", err)
	}

	ra, err := raft.NewRaft(
		config,
		&FSM{}, 
		logStore,
		logStore,
		snapshots,
		addr,
	)
	if err != nil {
		return fmt.Errorf("failed to create raft instance: %v", err)
	}
	store.raft = ra
	return nil
}


func main() {
	raftID := flag.String("raft-id", "node1", "Raft node ID")
	httpPort := flag.Int("http-port", 8081, "HTTP API port")
	raftPort := flag.Int("raft-port", 12000, "Raft communication port")
	raftDB := flag.String("raft-db", "raft.db", "Raft database path")
	joinAddr := flag.String("join", "", "Existing cluster node to join")
	flag.Parse()

	if err := setupRaft(*raftID, *raftPort, *raftDB); err != nil {
		log.Fatalf("[Raft] Setup failed: %v", err)
	}

	if *joinAddr == "" {
		log.Println("[Raft] Bootstrapping new cluster")
		config := raft.Configuration{
			Servers: []raft.Server{
				{
					ID:      raft.ServerID(*raftID),
					Address: raft.ServerAddress(fmt.Sprintf("127.0.0.1:%d", *raftPort)),
				},
			},
		}
		future := store.raft.BootstrapCluster(config)
		if err := future.Error(); err != nil {
			log.Fatalf("[Raft] Bootstrap failed: %v", err)
		}
	} else {
		log.Printf("[Raft] Joining existing cluster at %s", *joinAddr)
		if err := joinRaftCluster(*joinAddr, fmt.Sprintf("127.0.0.1:%d", *raftPort), *raftID); err != nil {
			log.Fatalf("[Raft] Join failed: %v", err)
		}
	}

	r := mux.NewRouter()
	r.HandleFunc("/api/v1/printers", postPrinter).Methods("POST")
	r.HandleFunc("/api/v1/printers", getPrinters).Methods("GET")
	r.HandleFunc("/api/v1/filaments", postFilament).Methods("POST")
	r.HandleFunc("/api/v1/filaments", getFilaments).Methods("GET")
	r.HandleFunc("/api/v1/print_jobs", postPrintJob).Methods("POST")
	r.HandleFunc("/api/v1/print_jobs", getPrintJobs).Methods("GET")
	r.HandleFunc("/api/v1/print_jobs/{id}/status", updateJobStatus).Methods("POST")
	r.HandleFunc("/api/v1/dev/clear", handleClearStore).Methods("POST")
	r.HandleFunc("/join", handleJoin).Methods("GET")

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("[System] Shutting down...")
		close(shutdownCh)
		os.Exit(0)
	}()

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", *httpPort),
		Handler: r,
	}
	
	log.Printf("[HTTP] Starting server on :%d", *httpPort)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("[HTTP] Server error: %v", err)
	}
}
