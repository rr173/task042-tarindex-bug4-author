# task042-tarindex

压缩包内容索引与搜索服务，仅使用标准库实现：在不解压、不落盘的前提下，以流式方式读过一遍 gzip 压缩的 tar 包，提取每个条目（文件、目录、符号链接等）的名称、大小、类型、权限位与修改时间，建成一份纯内存索引，供后续按名称通配、大小范围、类型筛选与分页检索。服务只读压缩包，无数据库与外网依赖。

## 主要输入输出

- 输入：`POST /archives` 上传一段 gzip 压缩的 tar 字节流（`Content-Type: application/gzip` 或 `application/octet-stream`）。
- 输出：上传成功返回 `{"id","entries","total_size","by_type"}`；检索返回分页后的条目数组与匹配总数。

## 标准本地命令

```bash
go build ./...          # 编译
go run . --smoke-test   # 自检（执行后自行退出）
go run . server --addr :8080   # 启动 HTTP 服务
go test ./...           # 测试；项目自带 selfcheck，无独立 *_test.go
```

## Docker 镜像构建

构建脚本 `build_benzhi_docker.sh` 接受两个参数：镜像名与目标平台。

```bash
# amd64
bash ./build_benzhi_docker.sh go-task-benzhi:amd64 linux/amd64
docker run -it go-task-benzhi:amd64

# arm64
bash ./build_benzhi_docker.sh go-task-benzhi:arm64 linux/arm64
docker run -it go-task-benzhi:arm64
```

进入容器后可执行 `go version`、`go build ./...` 等。

## 自检入口

`--smoke-test` 会运行内置的端到端 selfcheck（覆盖上传摘要、类型/名称/大小过滤、排序、分页、同名不去重、符号链接、空包、非法 gzip/非 tar、非法通配、404、删除等场景），通过后打印 `smoke-test PASSED` 并退出。
