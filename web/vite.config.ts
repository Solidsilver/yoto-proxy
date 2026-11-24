import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

// In dev, the Go backend runs on :3614 and serves the API, OAuth callbacks and streams.
const backend = 'http://127.0.0.1:3614';

export default defineConfig({
	plugins: [
		sveltekit({
			compilerOptions: {
				// Force runes mode for the project, except for libraries. Can be removed in svelte 6.
				runes: ({ filename }) =>
					filename.split(/[/\\]/).includes('node_modules') ? undefined : true
			},
			// Static SPA build, embedded into the Go binary (see embed.go).
			adapter: adapter({ fallback: 'index.html' })
		})
	],
	server: {
		proxy: {
			'/api': backend,
			'/auth': backend,
			'/s/': backend
		}
	}
});
