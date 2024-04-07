# github-act-nexus-cache

This code based on https://github.com/nektos/act `pkg/artifactcache`, and it add additional
function that will try to read cache from and commit cache to Nexus Repository Manager 3.

## Usage

The following environment variable is required to set the Nexus store path

```shell
export NEXUS_STORE_ENDPOINT=https://nxrm.mobilesolutionworks.com/repository/gh-action-cache/act-nexus-cache
export NEXUS_USERNAME=gh
export NEXUS_SECRET=gh
```

The following code is how the I used it as part as the execution.

```shell
#!/bin/bash

# Define cleanup function
cleanup() {
    echo "Cleaning up..."
    # Terminate the background process
    kill $bg_pid
}

# Trap signals: EXIT for normal script termination, INT for interrupt (Ctrl-C), and TERM for termination signal
trap cleanup EXIT INT TERM

# Start the background process
./act-nexus-cache &

# Capture the PID of the background process
bg_pid=$!

# Set -e to exit the script if any command fails
set -e

# Execute your certain task (multi-line)
 ~/bin/act -W .github/workflows/works.yaml -P ubuntu-latest=catthehacker/ubuntu:act-latest
# Add more commands as needed

# The cleanup function will be called automatically at this point due to the trap

```