<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { api, type Invite } from '#lib/api.ts';

	let invite = $state<Invite | null>(null);
	let error = $state('');
	let joining = $state(false);

	$effect(() => {
		api.invite(page.params.token ?? '').then(
			(i) => (invite = i),
			(e) => (error = e.message)
		);
	});

	async function join() {
		joining = true;
		try {
			await api.acceptInvite(page.params.token ?? '');
			await goto('/');
		} catch (e) {
			error = (e as Error).message;
			joining = false;
		}
	}
</script>

<div class="page">
	<div class="card">
		<img class="logo" src="/logo.png" alt="" width="48" height="48" />
		{#if error}
			<h1>Invite unavailable</h1>
			<p class="muted">{error}</p>
			<a class="btn" href="/">Go to Yoto Proxy</a>
		{:else if !invite}
			<p class="muted">Loading invite…</p>
		{:else if invite.alreadyMember}
			<h1>You’re already in “{invite.label}”</h1>
			<a class="btn primary" href="/">Open Yoto Proxy</a>
		{:else}
			<h1>Join “{invite.label}”</h1>
			<p>
				<strong>{invite.invitedBy}</strong> invited you to make cards for their Yoto account. You’ll share
				its cards with {invite.members.join(', ') || 'them'}.
			</p>
			<p class="muted small">
				Cards you make use your own Navidrome and Audiobookshelf libraries.
			</p>
			{#if invite.inOtherYoto}
				<p class="warn">
					You’re currently in another Yoto account. Joining moves you here; that account’s cards stay
					with it.
				</p>
			{/if}
			<div class="actions">
				<button class="btn primary" onclick={join} disabled={joining}>
					{joining ? 'Joining…' : 'Join'}
				</button>
				<a class="btn" href="/">Not now</a>
			</div>
		{/if}
	</div>
</div>

<style>
	.page {
		min-height: 100dvh;
		display: grid;
		place-items: center;
		padding: 16px;
	}
	.card {
		width: min(440px, 100%);
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		box-shadow: var(--shadow);
		padding: 1.5rem;
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}
	h1 {
		font-size: 1.3rem;
	}
	p {
		margin: 0;
	}
	.small {
		font-size: 0.875rem;
	}
	.warn {
		padding: 0.6rem 0.75rem;
		border-radius: 10px;
		background: var(--accent-soft);
		font-size: 0.9rem;
	}
	.actions {
		display: flex;
		gap: 0.5rem;
		margin-top: 0.25rem;
	}
	.logo {
		width: 48px;
		height: 48px;
	}
</style>
