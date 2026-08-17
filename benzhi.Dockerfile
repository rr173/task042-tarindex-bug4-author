# 官方 Go 镜像，自带完整工具链
FROM golang:1.26.3

WORKDIR /app

COPY . .
# 预编译一次，把编译缓存留在镜像里；不影响模型修改源码
RUN go build ./...

# 容器启动后进入 shell，方便操作
CMD ["bash"]
