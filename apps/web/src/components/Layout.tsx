import type { FC } from 'react';
import { Outlet } from 'react-router-dom';
import { Header } from './Header';
import { Footer } from './Footer';
import './Layout.css';

interface LayoutProps {
  user?: { plexUsername: string; plexThumb?: string } | null;
  onLogout?: () => void;
}

export const Layout: FC<LayoutProps> = ({ user, onLogout }) => {
  return (
    <div className="layout">
      <Header user={user} onLogout={onLogout} />
      <main className="layout-main">
        <Outlet />
      </main>
      <Footer />
    </div>
  );
};
