
# list targets and exit
default:
    just --list

# run the server
run:
    go run main.go

# build locally
build:
    go build -o bin/shutdown

# containerize
containerize tag="local":
    docker build -t shutdown:{{tag}} .

# bring up compose
up *flags="":
    docker-compose up {{flags}}
