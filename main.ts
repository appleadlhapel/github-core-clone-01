import { createApp } from './app.ts';

const environmentUrl = (process.env.MAKEFUN_ENV_URL ?? '').replace(/\/+$/, '');

if (!environmentUrl) {
  process.stderr.write('MAKEFUN_ENV_URL is required; no forge environment is available.\n');
  process.exit(1);
} else {
  const server = createApp();

  const wait = (milliseconds: number) => new Promise<void>((resolve) => setTimeout(resolve, milliseconds));

  const listen = () => new Promise<void>((resolve, reject) => {
    const onError = (error: Error) => {
      server.off('listening', onListening);
      reject(error);
    };
    const onListening = () => {
      server.off('error', onError);
      resolve();
    };
    server.once('error', onError);
    server.once('listening', onListening);
    server.listen(0);
  });

  const stop = () => new Promise<void>((resolve) => server.close(() => resolve()));

  const run = async () => {
    await listen();
    const address = server.address();
    if (address === null || typeof address === 'string') throw new Error('The HTTP server did not provide a TCP port.');
    const port = address.port;
    let delay = 250;

    for (;;) {
      try {
        const response = await fetch(`${environmentUrl}/register`, {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({ port }),
          signal: AbortSignal.timeout(30_000),
        });
        if (response.status === 409 || response.ok) break;
        await response.arrayBuffer().catch(() => undefined);
      } catch {
        // Registration may be temporarily unavailable while the environment starts.
      }
      await wait(delay);
      delay = Math.min(delay * 2, 5_000);
    }

    delay = 250;
    for (;;) {
      let complete = false;
      try {
        const response = await fetch(`${environmentUrl}/done`, { signal: AbortSignal.timeout(30_000) });
        if (response.ok) {
          const state: unknown = await response.json();
          complete = typeof state === 'object' && state !== null && 'done' in state && state.done === true;
        } else {
          await response.arrayBuffer().catch(() => undefined);
        }
      } catch {
        // A failed or timed out check is retried without ending the forge process.
      }

      if (complete) break;
      await wait(delay);
      delay = Math.min(delay * 2, 5_000);
    }

    await stop();
    process.exit(0);
  };

  run().catch((error: unknown) => {
    process.stderr.write(`Forge lifecycle failed: ${error instanceof Error ? error.message : String(error)}\n`);
    process.exit(1);
  });
}
