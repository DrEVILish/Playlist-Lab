import type { FC } from 'react';
import { useState, useEffect } from 'react';
import { NavLink, useLocation } from 'react-router-dom';
import { Footer } from './Footer';
import './MobileNav.css';

interface MobileNavProps {
  user?: { plexUsername: string; plexThumb?: string } | null;
  onLogout?: () => void;
}

export const MobileNav: FC<MobileNavProps> = ({ user, onLogout }) => {
  const location = useLocation();
  const [isOpen, setIsOpen] = useState(false);

  // Close menu when route changes
  useEffect(() => {
    setIsOpen(false);
  }, [location.pathname]);

  // Prevent body scroll when menu is open
  useEffect(() => {
    if (isOpen) {
      document.body.style.overflow = 'hidden';
    } else {
      document.body.style.overflow = '';
    }
    return () => {
      document.body.style.overflow = '';
    };
  }, [isOpen]);

  const navItems = [
    { path: '/', label: 'Home' },
    { path: '/import', label: 'Import' },
    { path: '/generate', label: 'Generate Mixes' },
    { path: '/shared-with-me', label: 'Shared With Me' },
    { path: '/backup', label: 'Backup / Restore' },
    { path: '/status', label: 'Status' },
    { path: '/settings', label: 'Settings' },
  ];

  return (
    <>
      {/* Hidden while open: this button sits in the page header, outside
          the slide-out panel, and its z-index has to stay above the page
          content for the closed (hamburger) state to be clickable - but
          that same z-index then floats it above the open panel too, right
          on top of the panel's own "Menu" heading and close button. The
          panel already has its own close button once open, so there's no
          need to keep this one visible/interactive at the same time. */}
      {!isOpen && (
        <button
          className="mobile-nav-toggle"
          onClick={() => setIsOpen(true)}
          aria-label="Open navigation menu"
          aria-expanded={false}
        >
          <span className="hamburger">
            <span></span>
            <span></span>
            <span></span>
          </span>
        </button>
      )}

      {isOpen && (
        <div className="mobile-nav-overlay" onClick={() => setIsOpen(false)} />
      )}

      <nav className={`mobile-nav ${isOpen ? 'mobile-nav-open' : ''}`}>
        <div className="mobile-nav-header">
          <h2>Menu</h2>
          <button
            className="mobile-nav-close"
            onClick={() => setIsOpen(false)}
            aria-label="Close menu"
          >
            ✕
          </button>
        </div>

        <div className="mobile-nav-content">
          {navItems.map((item) => (
            <NavLink
              key={item.path}
              to={item.path}
              className={({ isActive }) =>
                `mobile-nav-link ${isActive ? 'mobile-nav-link-active' : ''}`
              }
              end={item.path === '/'}
            >
              {item.label}
            </NavLink>
          ))}
        </div>

        <div className="mobile-nav-footer">
          {user && (
            <div className="mobile-nav-user">
              {user.plexThumb && (
                <img src={user.plexThumb} alt="" className="mobile-nav-user-avatar" />
              )}
              <span className="mobile-nav-user-name">{user.plexUsername}</span>
              {onLogout && (
                <button onClick={onLogout} className="btn btn-secondary btn-logout">Logout</button>
              )}
            </div>
          )}
          <Footer />
        </div>
      </nav>
    </>
  );
};
