#!/bin/sh
set -eu

# Deploys the Isoform front (docs/front-deploy.md). It answers every
# *.agents.isoform.ai connection, so the hub must approve each deploy. Run
# it yourself, signed in to Fly; no agent runs it.
if [ "${ISOFORM_DEPLOY_APPROVED:-}" != "yes" ]; then
	printf '%s\n' 'Deployment needs the hub'"'"'s OK. Rerun with ISOFORM_DEPLOY_APPROVED=yes after it is given. See docs/front-deploy.md.' >&2
	exit 1
fi
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$script_dir/.."
exec fly deploy --config fly.toml --ha=false
