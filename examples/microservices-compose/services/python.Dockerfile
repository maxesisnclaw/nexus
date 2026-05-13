# Generic Python service Dockerfile. Use --build-arg SVC=auth|catalog|notifier.
# Build context: repo root.
FROM python:3.12-slim

ARG SVC
ENV SVC=${SVC}

# The cross-namespace UDS fix landed in nexus-rpc-sdk v0.5.2. We pin
# >=0.5.2 so this demo always picks up a compatible SDK.
RUN pip install --no-cache-dir "nexus-rpc-sdk>=0.5.2" "msgpack>=1.0"

WORKDIR /app
COPY examples/microservices-compose/services/${SVC}/main.py /app/main.py

ENTRYPOINT ["python", "-u", "/app/main.py"]
