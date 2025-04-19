from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from typing import List
import requests
from enum import Enum

app = FastAPI()

GO_SERVER_PORTS = [8081, 8082, 8083]


class JobStatus(str, Enum):
    queued = "Queued"
    running = "Running"
    done = "Done"
    canceled = "Canceled"


class Printer(BaseModel):
    id: str
    company: str
    model: str

class Filament(BaseModel):
    id: str
    type: str
    color: str
    total_weight_in_grams: int
    remaining_weight_in_grams: int

class PrintJob(BaseModel):
    id: str
    printer_id: str
    filament_id: str
    filepath: str
    print_weight_in_grams: int
    status: JobStatus

def send_request(method, endpoint, **kwargs):
    for port in GO_SERVER_PORTS:
        try:
            url = f"http://localhost:{port}{endpoint}"
            response = requests.request(method, url, timeout=3, **kwargs)
            if response.ok:
                return response
        except requests.exceptions.RequestException:
            continue
    raise HTTPException(status_code=503, detail="All Raft nodes are unreachable or not the leader.")


@app.get("/")
def read_root():
    return {"message": "Visualization of Raft3D endpoints with FastAPI"}


@app.post("/printers")
def create_printer(printer: Printer):
    response = send_request("POST", "/api/v1/printers", json=printer.dict())
    return {"message": "Printer added successfully"}

@app.get("/printers")
def list_printers():
    response = send_request("GET", "/api/v1/printers")
    return response.json()


@app.post("/filaments")
def create_filament(filament: Filament):
    response = send_request("POST", "/api/v1/filaments", json=filament.dict())
    return {"message": "Filament added successfully"}

@app.get("/filaments")
def list_filaments():
    response = send_request("GET", "/api/v1/filaments")
    return response.json()


@app.post("/print_jobs")
def create_print_job(job: PrintJob):
    response = send_request("POST", "/api/v1/print_jobs", json=job.dict())
    return {"message": "Print job added successfully"}

@app.get("/print_jobs")
def list_print_jobs():
    response = send_request("GET", "/api/v1/print_jobs")
    return response.json()

@app.post("/print_jobs/{job_id}/status")
def update_job_status(job_id: str, status: JobStatus):
    response = send_request(
        "POST",
        f"/api/v1/print_jobs/{job_id}/status",
        params={"status": status.value}
        )
    if response.ok:
        return {"message": f"Status of job {job_id} updated to '{status}'"}
    return {"message": f"Invalid state transition"}



