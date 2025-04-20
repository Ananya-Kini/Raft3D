# Raft3D
3D Printer Management using Raft Consensus Algorithm

Steps to run:
1. ```go build -o raft3d kvstore.go```
2. ```./raft3d --raft-id node1 --raft-port 12000 --http-port 8081 --raft-db raft1.db```
3. ```./raft3d --raft-id node2 --raft-port 12001 --http-port 8082 --raft-db raft2.db --join 127.0.0.1:8081```
4. ```./raft3d --raft-id node3 --raft-port 12002 --http-port 8083 --raft-db raft3.db --join 127.0.0.1:8081```
5. ```uvicorn main:app --reload```

(or)

Run the `start.sh` directly:
- ```chmod +x start.sh```
- ```./start.sh```
