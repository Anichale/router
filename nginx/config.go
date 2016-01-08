package nginx

import (
	"io/ioutil"
	"os"
	"path"
	"text/template"

	"github.com/Masterminds/sprig"
	"github.com/deis/router/model"
)

const (
	confTemplate = `{{ $routerConfig := . }}user nginx;
daemon off;
pid /run/nginx.pid;
worker_processes {{ $routerConfig.WorkerProcesses }};

events {
	worker_connections {{ $routerConfig.MaxWorkerConnections }};
	# multi_accept on;
}

http {
	# basic settings
	sendfile on;
	tcp_nopush on;
	tcp_nodelay on;

	# The timeout value must be greater than the front facing load balancers timeout value.
	# Default is the deis recommended timeout value for ELB - 1200 seconds + 100s extra.
	keepalive_timeout {{ $routerConfig.DefaultTimeout }}s;

	types_hash_max_size 2048;
	server_names_hash_max_size {{ $routerConfig.ServerNameHashMaxSize }};
	server_names_hash_bucket_size {{ $routerConfig.ServerNameHashBucketSize }};

	{{ if $routerConfig.GzipConfig }}{{ $gzipConfig := $routerConfig.GzipConfig }}gzip on;
	gzip_comp_level {{ $gzipConfig.CompLevel }};
	gzip_disable {{ $gzipConfig.Disable }};
	gzip_http_version {{ $gzipConfig.HTTPVersion }};
	gzip_min_length {{ $gzipConfig.MinLength }};
	gzip_types {{ $gzipConfig.Types }};
	gzip_proxied {{ $gzipConfig.Proxied }};
	gzip_vary {{ $gzipConfig.Vary }};{{ end }}

	client_max_body_size {{ $routerConfig.BodySize }}m;

	{{ if $routerConfig.UseProxyProtocol }}set_real_ip_from {{ $routerConfig.ProxyRealIPCIDR }};
	real_ip_header proxy_protocol;
	{{ end }}

	log_format upstreaminfo '[$time_local] - {{ if $routerConfig.UseProxyProtocol }}$proxy_protocol_addr{{ else }}$remote_addr{{ end }} - $remote_user - $status - "$request" - $bytes_sent - "$http_referer" - "$http_user_agent" - "$server_name" - $upstream_addr - $http_host - $upstream_response_time - $request_time';

	access_log /opt/nginx/logs/access.log upstreaminfo;
	error_log  /opt/nginx/logs/error.log {{ $routerConfig.ErrorLogLevel }};

	map $http_upgrade $connection_upgrade {
		default upgrade;
		'' close;
	}

	# Default server handles requests for unmapped hostnames, including healthchecks
	server {
		listen 80 default_server reuseport{{ if $routerConfig.UseProxyProtocol }} proxy_protocol{{ end }};
		{{ if $routerConfig.PlatformCertificate }}
		listen 443 default_server ssl{{ if $routerConfig.UseProxyProtocol }} proxy_protocol{{ end }};
		ssl_protocols TLSv1 TLSv1.1 TLSv1.2;
		ssl_certificate /opt/nginx/ssl/server.crt;
		ssl_certificate_key /opt/nginx/ssl/server.key;
		{{ end }}
		server_name _;
		location ~ ^/healthz/?$ {
			access_log off;
			default_type 'text/plain';
			return 200;
		}
		location / {
			return 404;
		}
	}

	# Healthcheck on 9090 -- never uses proxy_protocol
	server {
		listen 9090 default_server;
		server_name _;
		location ~ ^/healthz/?$ {
			access_log off;
			default_type 'text/plain';
			return 200;
		}
		location / {
			return 404;
		}
	}

	{{range $appConfig := $routerConfig.AppConfigs}}{{range $domain := $appConfig.Domains}}server {
		listen 80{{ if $routerConfig.UseProxyProtocol }} proxy_protocol{{ end }};
		server_name {{ if contains "." $domain }}{{ $domain }}{{ else if ne $routerConfig.Domain "" }}{{ $domain }}.{{ $routerConfig.Domain }}{{ else }}~^{{ $domain }}\.(?<domain>.+)${{ end }};
		server_name_in_redirect off;
		port_in_redirect off;

		{{ if and $routerConfig.PlatformCertificate (not (contains "." $domain)) }}
		listen 443 ssl{{ if $routerConfig.UseProxyProtocol }} proxy_protocol{{ end }};
		ssl_protocols TLSv1 TLSv1.1 TLSv1.2;
		ssl_certificate /opt/nginx/ssl/server.crt;
		ssl_certificate_key /opt/nginx/ssl/server.key;
		{{ end }}

		{{ if and $routerConfig.EnforceWhitelists (ne (len $appConfig.Whitelist) 0) }}{{ range $whitelistEntry := $appConfig.Whitelist }}
		allow {{ $whitelistEntry }};{{ end }}
		deny all;{{ end }}

		location / {
			proxy_buffering off;
			proxy_set_header Host $host;
			proxy_set_header X-Forwarded-For {{ if $routerConfig.UseProxyProtocol }}$proxy_protocol_addr{{ else }}$proxy_add_x_forwarded_for{{ end }};
			proxy_redirect off;
			proxy_connect_timeout {{ $appConfig.ConnectTimeout }}s;
			proxy_send_timeout {{ $appConfig.TCPTimeout }}s;
			proxy_read_timeout {{ $appConfig.TCPTimeout }}s;
			proxy_http_version 1.1;
			proxy_set_header Upgrade $http_upgrade;
			proxy_set_header Connection $connection_upgrade;
			proxy_pass http://{{$appConfig.ServiceIP}}:80;
		}
	}

	{{end}}{{end}}
}

{{ if $routerConfig.BuilderConfig }}{{ $builderConfig := $routerConfig.BuilderConfig }}stream {
	server {
		listen 2222;
		proxy_connect_timeout {{ $builderConfig.ConnectTimeout }}s;
		proxy_timeout {{ $builderConfig.TCPTimeout }}s;
		proxy_pass {{$builderConfig.ServiceIP}}:2222;
	}
}{{ end }}
`
)

func WriteCerts(routerConfig *model.RouterConfig, sslPath string) error {
	platformCertPath := path.Join(sslPath, "server.crt")
	platformKeyPath := path.Join(sslPath, "server.key")
	if routerConfig.PlatformCertificate != nil {
		// Write the cert and key
		err := ioutil.WriteFile(platformCertPath, []byte(routerConfig.PlatformCertificate.Cert), 0644)
		if err != nil {
			return err
		}
		err = ioutil.WriteFile(platformKeyPath, []byte(routerConfig.PlatformCertificate.Key), 0600)
		if err != nil {
			return err
		}
	} else {
		// Delete the cert and key
		os.Remove(platformCertPath)
		os.Remove(platformKeyPath)
	}
	return nil
}

// WriteConfig dynamically produces valid nginx configuration by combining a Router configuration
// object with a data-driven template.
func WriteConfig(routerConfig *model.RouterConfig, filePath string) error {
	tmpl, err := template.New("nginx").Funcs(sprig.TxtFuncMap()).Parse(confTemplate)
	if err != nil {
		return err
	}
	file, err := os.Create(filePath)
	if err != nil {
		return err
	}
	err = tmpl.Execute(file, routerConfig)
	if err != nil {
		return err
	}
	return nil
}
