"""Low-rate real HTTP and gRPC traffic for the persistent Edge demo."""
import json
import sys
import time
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor

import grpc
import health_pb2
import health_pb2_grpc


def run(language, port, rpc, check=False):
    with grpc.insecure_channel(f"127.0.0.1:{port + 1}") as channel:
        client = health_pb2_grpc.HealthStub(channel)
        iteration = 0
        while not check or iteration < 10:
            iteration += 1
            failed, slow = iteration % 10 == 0, iteration % 5 == 0
            suffix = "?fail=1" if failed else "?slow=1" if slow else ""
            try:
                with urllib.request.urlopen(f"http://127.0.0.1:{port}/orders/42{suffix}", timeout=5) as response:
                    response.read()
                    assert response.status == 200 and not failed
            except urllib.error.HTTPError as error:
                if error.code != 500 or not failed:
                    if check:
                        raise
                    print(json.dumps({"language": language, "http_error": str(error)}), flush=True)
            except (OSError, AssertionError) as error:
                if check:
                    raise
                print(json.dumps({"language": language, "http_error": str(error)}), flush=True)
            if rpc:
                try:
                    client.Check(health_pb2.HealthCheckRequest(service="missing" if failed else "slow" if slow and language != "go" else ""), timeout=5)
                    assert not failed, "expected gRPC NOT_FOUND"
                except grpc.RpcError as error:
                    if not failed or error.code() != grpc.StatusCode.NOT_FOUND:
                        if check:
                            raise
                        print(json.dumps({"language": language, "rpc_error": str(error)}), flush=True)
            if iteration % 30 == 0:
                print(json.dumps({"language": language, "iterations": iteration}), flush=True)
            time.sleep(2)


if __name__ == "__main__":
    with ThreadPoolExecutor(max_workers=9) as pool:
        jobs = [pool.submit(run, language, port, language in {"go", "java", "node", "python"}, "--check" in sys.argv) for language, port in [("go", 18080), ("java", 18082), ("node", 18084), ("python", 18086), ("dotnet", 18088), ("php", 18090), ("cpp", 18092), ("rust", 18094), ("ruby", 18096)]]
        for job in jobs:
            job.result()
