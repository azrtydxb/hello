# A GitHub service container cannot pass arguments to the image's
# entrypoint, and the upstream MinIO image's default command only prints
# its help. This adds the server command the entrypoint wants; the
# credentials come from the service's env (MINIO_ROOT_USER/PASSWORD).
FROM minio/minio:latest
CMD ["server", "/data"]
