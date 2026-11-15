import path from 'path';
import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { defineConfig, type Plugin } from 'vite';

const defaultPort = 5173;
const envPort = Number(process.env.VITE_PORT ?? process.env.PORT);
const port = Number.isFinite(envPort) && envPort > 0 ? envPort : defaultPort;

// Vite plugin that always intercepts `wailsjs/` imports at resolve time and
// redirects them to the unified shim modules. The shims detect at runtime
// whether Wails native bindings are present on `window` and dispatch to them
// directly, otherwise they fall back to the HTTP browser bridge. This means
// a single `pnpm run build` produces a frontend that works in both the Wails
// WebView and a regular browser -- no BROWSER_MODE env var needed.
function wailsBrowserShimPlugin(): Plugin {
    const shimAppPath = path.resolve(__dirname, './src/lib/wails-shim-app.ts');
    const shimRuntimePath = path.resolve(__dirname, './src/lib/wails-shim-runtime.ts');
    const shimModelsPath = path.resolve(__dirname, './src/lib/wails-shim-models.ts');

    const shimMap: Record<string, string> = {
        'wailsjs/go/main/App': shimAppPath,
        'wailsjs/runtime/runtime': shimRuntimePath,
        'wailsjs/go/models': shimModelsPath,
    };

    // Files that are allowed to import the real `wailsjs/go/models` directly
    // (otherwise the shim would recursively resolve to itself).
    const shimFiles = new Set([shimAppPath, shimRuntimePath, shimModelsPath]);

    return {
        name: 'wails-browser-shim',
        enforce: 'pre',
        resolveId(source, importer) {
            const normalizedImporter = importer ? importer.split('?')[0]!.split('#')[0]! : null;

            // Let the shim files themselves import the real wailsjs modules
            // without being re-intercepted.
            if (normalizedImporter && shimFiles.has(path.resolve(normalizedImporter))) {
                return null;
            }

            for (const [pattern, shimPath] of Object.entries(shimMap)) {
                if (source.includes(pattern)) {
                    return shimPath;
                }
            }

            if (normalizedImporter && source.startsWith('.')) {
                const resolved = path.resolve(path.dirname(normalizedImporter), source);
                for (const [pattern, shimPath] of Object.entries(shimMap)) {
                    if (resolved.includes(pattern)) {
                        return shimPath;
                    }
                }
            }

            return null;
        },
    };
}

export default defineConfig({
    plugins: [react(), tailwindcss(), wailsBrowserShimPlugin()],
    build: {
        outDir: 'dist',
        emptyOutDir: true,
    },
    server: {
        port,
        strictPort: false,
        hmr: {
            protocol: 'ws',
            host: 'localhost',
        },
    },
    resolve: {
        alias: {
            '@': path.resolve(__dirname, './src'),
            '@wails': path.resolve(__dirname, '..'),
            '@wails-generated-app': path.resolve(__dirname, './wailsjs/go/main/App.js'),
            '@wails-generated-models': path.resolve(__dirname, './wailsjs/go/models.ts'),
        },
    },
    test: {
        environment: 'jsdom',
        setupFiles: ['./src/test-setup.ts'],
        // Background jobs (the merge queue's launchd runner) can report one CPU,
        // which makes vitest's default worker range invalid. Pin it.
        poolOptions: {
            forks: { minForks: 1, maxForks: 4 },
            threads: { minThreads: 1, maxThreads: 4 },
        },
        globals: true,
        // Playwright e2e specs live under e2e/ and are run via playwright,
        // not vitest. Exclude them so vitest doesn't try to resolve
        // @playwright/test as a regular vitest suite.
        include: ['src/**/*.{test,spec}.{ts,tsx}'],
        exclude: ['e2e/**', 'node_modules/**', 'dist/**'],
    },
});
