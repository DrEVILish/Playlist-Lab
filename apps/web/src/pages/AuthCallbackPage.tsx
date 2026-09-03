import type { FC } from 'react';
import { useEffect } from 'react';

export const AuthCallbackPage: FC = () => {
  useEffect(() => {
    // Plex forwards the sign-in popup here when it's done. Tell the login
    // page (which is polling for the token) and close ourselves.
    //
    // window.opener can be null even in a real popup if the browser severed
    // the opener across the cross-origin trip through plex.tv, so never fall
    // back to rendering the login page here - that's what left the popup
    // sitting on a second "Sign in with Plex" screen. close() still works for
    // a script-opened window; the parent's polling completes the login either
    // way.
    try {
      window.opener?.postMessage({ type: 'plex-auth-complete' }, window.location.origin);
    } catch {
      // Cross-origin safety - ignore
    }
    window.close();
  }, []);

  return (
    <div style={{
      display: 'flex',
      justifyContent: 'center',
      alignItems: 'center',
      minHeight: '100vh'
    }}>
      <div style={{ textAlign: 'center' }}>
        <h2>Signed in</h2>
        <p style={{ color: 'var(--text-secondary)' }}>You can close this window.</p>
      </div>
    </div>
  );
};
