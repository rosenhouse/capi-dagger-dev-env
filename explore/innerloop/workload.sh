#!/usr/bin/env bash
# A workload-side controller that crashloops, and a non-Go image from the Images hook, in the edit loop.
source "$(dirname "$0")/common.sh"
setup_greeting
cd "$G"
mkdir -p web config/web
echo '<p>web v1</p>' >web/index.html
cat >config/web/web.yaml <<'EOF'
apiVersion: v1
kind: Namespace
metadata:
  name: web
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: web
spec:
  selector:
    matchLabels: {app: web}
  template:
    metadata:
      labels: {app: web}
    spec:
      containers:
      - name: web
        image: web
EOF
perl -0pi -e 's/(import \(\n)/$1\t"dagger.io\/dagger"\n/; s/(\tPackages: \[\]devenv.Package\{\n)/$1\t\t{Name: "web", RefName: "web.demo.example.com", Config: "config\/web", Images: []string{"web"}},\n/; s/(\tReady: remoteInstall,)/\tImages: func(c *dagger.Client, src *dagger.Directory, _ string) map[string]*dagger.Container {\n\t\treturn map[string]*dagger.Container{"web": c.Container().From("nginx:1.29-alpine").WithFile("\/usr\/share\/nginx\/html\/index.html", src.File("web\/index.html"))}\n\t},\n$1/' cmd/devenv/main.go
sed -n 1,40p cmd/devenv/main.go
(cd "$G" && go build -o "$DEVENV" ./cmd/devenv) || { obs "CLI build with Images hook failed"; exit 1; }
up_bg
cd "$G"
greeting "hello one"
obs "hello serves after $(wait_hello 'hello one' 300)s"
web_body() { mk -n web exec deploy/web -- cat /usr/share/nginx/html/index.html 2>&1 | head -1; }
obs "web serves: $(web_body)"

section "W1: edit only the non-Go file"
snap "$W/w0"; mk -n web get pods --no-headers 2>&1 | awk '{print "mgmt/web/"$1}' >>"$W/w0"; sort -o "$W/w0" "$W/w0"
echo '<p>web v2</p>' >web/index.html
rd web-edit
snap "$W/w1"; mk -n web get pods --no-headers 2>&1 | awk '{print "mgmt/web/"$1}' >>"$W/w1"; sort -o "$W/w1" "$W/w1"
obs "web-edit rolled: $(rolled "$W/w0" "$W/w1")"
obs "web serves: $(web_body)"

section "W2: edit only Go code"
sed -i 's|"%s (hello %s)\\n"|"%s (hello %s) W2\\n"|' cmd/hello/main.go
rd go-edit
snap "$W/w2"; mk -n web get pods --no-headers 2>&1 | awk '{print "mgmt/web/"$1}' >>"$W/w2"; sort -o "$W/w2" "$W/w2"
obs "go-edit rolled: $(rolled "$W/w1" "$W/w2")"

section "W3: workload-side controller crashloops"
cp cmd/greeting-controller/main.go "$W/gc.bak"
sed -i 's|log := ctrl.Log.WithName("greeting-controller")|log := ctrl.Log.WithName("greeting-controller")\n\tlog.Error(nil, "explore: crashing on purpose"); os.Exit(3)|' cmd/greeting-controller/main.go
rd workload-crashloop
wk -n greeting-controller get pods 2>&1
obs "workload App: $(mk -n default get app work-greeting-controller -o jsonpath='{.status.friendlyDescription}' 2>&1)"
obs "hello still serves: $(hello_body)"

section "W4: fix it with a new state (not a revert)"
cp "$W/gc.bak" cmd/greeting-controller/main.go
sed -i 's|log := ctrl.Log.WithName("greeting-controller")|log := ctrl.Log.WithName("greeting-controller")\n\tlog.Info("explore W4 fix")|' cmd/greeting-controller/main.go
t=$SECONDS
rd workload-fix || rd workload-fix-retry
obs "workload recovered $((SECONDS - t))s after the fix started; pods: $(wk -n greeting-controller get pods --no-headers 2>&1 | tr '\n' ';')"

section "end"
"$DEVENV" down --name "$NAME"
summary
