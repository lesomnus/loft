# OpenSearch with nori, the Korean analyzer, which is what splits 웹서버 into
# the words a search for 서버 finds (D31, design 9.5). The official image, with
# the plugin of its own release beside it: nothing else about the server
# changes.
#
#   docker build -t loft-opensearch deploy/opensearch
#
# What it does not need it does not carry: every other plugin of the official
# image is taken out, since each one takes heap and threads that a node of a
# few gigabytes does not have to spare. The security plugin stays -- it is what
# makes a node ask who is calling and speak TLS (docs/dev/search.md).
ARG OPENSEARCH_VERSION=3.9.0

FROM opensearchproject/opensearch:${OPENSEARCH_VERSION}
ARG OPENSEARCH_VERSION
# The plugin's release, and what it has to hash to: a download can be
# replaced, a digest cannot.
ARG NORI_SHA256=18c132a9cd5dd6d090ca9187ed0a4f36f118b9dc55362d788076c4edf3f0e1b7
RUN curl -fsSL -o /tmp/analysis-nori.zip "https://artifacts.opensearch.org/releases/plugins/analysis-nori/${OPENSEARCH_VERSION}/analysis-nori-${OPENSEARCH_VERSION}.zip" \
	&& echo "${NORI_SHA256}  /tmp/analysis-nori.zip" | sha256sum -c - \
	&& /usr/share/opensearch/bin/opensearch-plugin install --batch file:///tmp/analysis-nori.zip \
	&& rm /tmp/analysis-nori.zip
# In passes, since a plugin another one needs is only taken out once that one
# is gone.
RUN cd /usr/share/opensearch \
	&& for i in 1 2 3 4; do \
		for p in $(bin/opensearch-plugin list); do \
			case "$p" in \
				analysis-nori|opensearch-security) ;; \
				*) bin/opensearch-plugin remove "$p" > /dev/null 2>&1 || true ;; \
			esac; \
		done; \
	done \
	&& test "$(bin/opensearch-plugin list | sort | tr '\n' ' ')" = "analysis-nori opensearch-security "
