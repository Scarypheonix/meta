// Mobile Navigation Toggle
document.getElementById('toggle-menu').addEventListener('click', function () {
    document.getElementById('nav-menu').classList.toggle('show');
});

// Dark Mode Toggle
document.getElementById('theme-toggle').addEventListener('click', function () {
    document.body.classList.toggle('dark-mode');
});

// Scroll Reveal Animation
const sections = document.querySelectorAll('section');
window.addEventListener('scroll', () => {
    const trigger = window.innerHeight * 0.8;
    sections.forEach(section => {
        const top = section.getBoundingClientRect().top;
        if (top < trigger) {
            section.classList.add('visible');
        }
    });
});

// Project Filter
document.querySelectorAll('#project-filters button').forEach(btn => {
    btn.addEventListener('click', () => {
        const filter = btn.dataset.filter;
        document.querySelectorAll('.project').forEach(project => {
            project.style.display =
                filter === 'all' || project.dataset.category === filter ? 'block' : 'none';
        });
    });
});

// Contact form validation and display
document.getElementById('contact-form').addEventListener('submit', function (e) {
    e.preventDefault();

    // Get form data
    const name = document.getElementById('name');
    const email = document.getElementById('email');
    const message = document.getElementById('message');

    // Simple validation
    const errors = [];
    if (name.value.trim().length < 2) {
        errors.push("Name must be at least 2 characters.");
    }

    const emailPattern = /^[^@\s]+@[^@\s]+\.[^@\s]+$/;
    if (!emailPattern.test(email.value.trim())) {
        errors.push("Please enter a valid email address.");
    }

    if (message.value.trim().length < 10) {
        errors.push("Message must be at least 10 characters.");
    }

    const responseBox = document.getElementById('form-response');

    if (errors.length > 0) {
        responseBox.innerHTML = errors.map(err => `<p style="color: red;">${err}</p>`).join('');
        responseBox.classList.remove('hidden');
    } else {
        
        responseBox.classList.remove('hidden');

        // Optionally reset the form after a delay
        setTimeout(() => {
            document.getElementById('contact-form').reset();
        }, 1000);
    }
});

;
// Smooth scroll
document.querySelectorAll('a[href^="#"]').forEach(anchor => {
    anchor.addEventListener('click', function (e) {
        e.preventDefault();
        document.querySelector(this.getAttribute('href')).scrollIntoView({
            behavior: 'smooth'
        });
    });
});

// Highlight active nav link
window.addEventListener('scroll', () => {
    const links = document.querySelectorAll('.nav-links a');
    const fromTop = window.scrollY + 80;

    links.forEach(link => {
        const section = document.querySelector(link.hash);
        if (
            section.offsetTop <= fromTop &&
            section.offsetTop + section.offsetHeight > fromTop
        ) {
            links.forEach(l => l.classList.remove('active'));
            link.classList.add('active');
        }
    });
});
