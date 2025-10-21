/**
 * File List Selector - Reusable component for file selection with keyboard shortcuts
 * Supports:
 * - Click to select individual items
 * - Shift+Click for range selection
 * - Ctrl/Cmd+Click for multi-select
 * - Select All / Deselect All buttons
 */

class FileListSelector {
    constructor(containerSelector, options = {}) {
        this.container = document.querySelector(containerSelector);
        if (!this.container) {
            console.error(`Container ${containerSelector} not found`);
            return;
        }
        
        this.options = {
            checkboxClass: options.checkboxClass || 'file-checkbox',
            selectAllBtn: options.selectAllBtn || null,
            deselectAllBtn: options.deselectAllBtn || null,
            onChange: options.onChange || null,
            itemSelector: options.itemSelector || '.file-item'
        };
        
        this.lastCheckedIndex = null;
        this.checkboxes = [];
        
        this.init();
    }
    
    init() {
        // Get all checkboxes
        this.refreshCheckboxes();
        
        // Add event listeners
        this.attachCheckboxListeners();
        this.attachButtonListeners();
    }
    
    refreshCheckboxes() {
        this.checkboxes = Array.from(
            this.container.querySelectorAll(`.${this.options.checkboxClass}`)
        );
    }
    
    attachCheckboxListeners() {
        this.checkboxes.forEach((checkbox, index) => {
            checkbox.addEventListener('click', (e) => {
                this.handleCheckboxClick(e, index);
            });
        });
    }
    
    handleCheckboxClick(event, currentIndex) {
        const checkbox = event.target;
        
        // Shift+Click: Range selection
        if (event.shiftKey && this.lastCheckedIndex !== null) {
            const start = Math.min(this.lastCheckedIndex, currentIndex);
            const end = Math.max(this.lastCheckedIndex, currentIndex);
            const checked = checkbox.checked;
            
            // Select all checkboxes in range
            for (let i = start; i <= end; i++) {
                this.checkboxes[i].checked = checked;
            }
        }
        // Ctrl/Cmd+Click: Multi-select (already handled by browser)
        // Regular Click: Single select (already handled by browser)
        
        this.lastCheckedIndex = currentIndex;
        
        // Trigger onChange callback
        if (this.options.onChange) {
            this.options.onChange(this.getSelectedValues());
        }
    }
    
    attachButtonListeners() {
        // Select All button
        if (this.options.selectAllBtn) {
            const selectAllBtn = document.querySelector(this.options.selectAllBtn);
            if (selectAllBtn) {
                selectAllBtn.addEventListener('click', () => {
                    this.selectAll();
                });
            }
        }
        
        // Deselect All button
        if (this.options.deselectAllBtn) {
            const deselectAllBtn = document.querySelector(this.options.deselectAllBtn);
            if (deselectAllBtn) {
                deselectAllBtn.addEventListener('click', () => {
                    this.deselectAll();
                });
            }
        }
    }
    
    selectAll() {
        this.checkboxes.forEach(checkbox => {
            checkbox.checked = true;
        });
        
        if (this.options.onChange) {
            this.options.onChange(this.getSelectedValues());
        }
    }
    
    deselectAll() {
        this.checkboxes.forEach(checkbox => {
            checkbox.checked = false;
        });
        
        if (this.options.onChange) {
            this.options.onChange(this.getSelectedValues());
        }
    }
    
    getSelectedValues() {
        return this.checkboxes
            .filter(checkbox => checkbox.checked)
            .map(checkbox => checkbox.value);
    }
    
    getSelectedCount() {
        return this.checkboxes.filter(checkbox => checkbox.checked).length;
    }
    
    getTotalCount() {
        return this.checkboxes.length;
    }
    
    // Method to update the list when items are added dynamically
    refresh() {
        this.refreshCheckboxes();
        this.attachCheckboxListeners();
    }
}

// Export for use in modules or make available globally
if (typeof module !== 'undefined' && module.exports) {
    module.exports = FileListSelector;
} else {
    window.FileListSelector = FileListSelector;
}
