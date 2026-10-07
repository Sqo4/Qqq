// Theme toggle
const root = document.documentElement;
document.getElementById('theme-toggle').addEventListener('click', () => {
  const isDark = root.dataset.theme
    ? root.dataset.theme === 'dark'
    : matchMedia('(prefers-color-scheme: dark)').matches;
  root.dataset.theme = isDark ? 'light' : 'dark';
  try { localStorage.setItem('theme', root.dataset.theme); } catch (e) {}
});

// Mobile menu
const nav = document.getElementById('nav');
const burger = document.getElementById('burger');
burger.addEventListener('click', () => {
  const open = nav.classList.toggle('is-open');
  burger.setAttribute('aria-expanded', open);
});
nav.addEventListener('click', (e) => {
  if (e.target.tagName === 'A') {
    nav.classList.remove('is-open');
    burger.setAttribute('aria-expanded', 'false');
  }
});

// Project filters
const chips = document.querySelectorAll('.chip');
const cards = document.querySelectorAll('.card');
chips.forEach((chip) => {
  chip.addEventListener('click', () => {
    chips.forEach((c) => c.classList.toggle('is-active', c === chip));
    const f = chip.dataset.filter;
    cards.forEach((card) => {
      card.classList.toggle('is-hidden', f !== 'all' && card.dataset.cat !== f);
    });
  });
});

// Reveal on scroll
const observer = new IntersectionObserver((entries) => {
  entries.forEach((entry) => {
    if (entry.isIntersecting) {
      entry.target.classList.add('is-visible');
      observer.unobserve(entry.target);
    }
  });
}, { threshold: 0.1 });
document.querySelectorAll('.reveal').forEach((el) => observer.observe(el));

// Contact form (client-side only — no backend)
const form = document.getElementById('contact-form');
const status = document.getElementById('form-status');
form.addEventListener('submit', (e) => {
  e.preventDefault();
  if (!form.checkValidity()) {
    status.textContent = 'Пожалуйста, заполните все поля корректно.';
    status.className = 'form__status err';
    return;
  }
  const data = new FormData(form);
  const subject = encodeURIComponent('Сообщение с сайта от ' + data.get('name'));
  const body = encodeURIComponent(data.get('message') + '\n\n' + data.get('email'));
  window.location.href = `mailto:hello@example.com?subject=${subject}&body=${body}`;
  status.textContent = 'Спасибо! Открываю почтовый клиент…';
  status.className = 'form__status ok';
  form.reset();
});

document.getElementById('year').textContent = new Date().getFullYear();
