PWD="$(cd `dirname $0`; pwd)"
NAME="x402-server"

rm -rf $NAME
rm -rf $NAME.tar.gz

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ./$NAME

echo "building finished!"

tar czvf $NAME.tar.gz $NAME

scp $NAME.tar.gz root@154.92.16.59:/root