#!/bin/bash

echo "Building raft3d from kvstore.go..."
go build -o raft3d kvstore.go

if [ $? -ne 0 ]; then
    echo "Build failed. Exiting script."
    exit 1
fi

echo "Build successful. Launching nodes and API..."

# Terminal for node1
gnome-terminal --title="Node 1" -- bash -c "
echo 'Starting Node 1...';
./raft3d --raft-id node1 --raft-port 12000 --http-port 8081 --raft-db raft1.db;
exec bash"

# Terminal for node2
gnome-terminal --title="Node 2" -- bash -c "
echo 'Starting Node 2...';
./raft3d --raft-id node2 --raft-port 12001 --http-port 8082 --raft-db raft2.db --join 127.0.0.1:8081;
exec bash"

# Terminal for node3
gnome-terminal --title="Node 3" -- bash -c "
echo 'Starting Node 3...';
./raft3d --raft-id node3 --raft-port 12002 --http-port 8083 --raft-db raft3.db --join 127.0.0.1:8081;
exec bash"

